package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"github.com/steipete/sonoscli/internal/sonos"
)

type watchEvent struct {
	Time    time.Time         `json:"time"`
	Service string            `json:"service"`
	SID     string            `json:"sid"`
	Seq     string            `json:"seq"`
	Vars    map[string]string `json:"vars"`
}

func listenIPForRemote(remoteIP string) (string, error) {
	conn, err := net.Dial("udp", net.JoinHostPort(remoteIP, "1900"))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || udpAddr.IP == nil {
		return "", errors.New("could not determine local listen ip")
	}
	return udpAddr.IP.String(), nil
}

func newWatchCmd(flags *rootFlags) *cobra.Command {
	var duration time.Duration
	var natsURL string
	var natsSubject string
	var callbackIP string
	var listenPort int

	cmd := &cobra.Command{
		Use:          "watch",
		Short:        "Watch live Sonos events",
		Long:         "Subscribes to AVTransport and RenderingControl events and prints changes as they arrive (Ctrl+C to stop). Requires that Sonos speakers can reach your machine on the chosen callback port (firewall may prompt).",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTarget(flags); err != nil {
				return err
			}

			ctx := cmd.Context()
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
			defer stop()
			if duration > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, duration)
				defer cancel()
			}

			c, err := coordinatorClient(ctx, flags)
			if err != nil {
				return err
			}

			var bindAddr string
			var cbIP string
			if callbackIP != "" {
				// In K8s with MetalLB: bind all interfaces, use the
				// external LB IP in the callback URL sent to Sonos.
				bindAddr = fmt.Sprintf("0.0.0.0:%d", listenPort)
				cbIP = callbackIP
			} else {
				autoIP, ipErr := listenIPForRemote(c.IP)
				if ipErr != nil {
					return ipErr
				}
				bindAddr = net.JoinHostPort(autoIP, fmt.Sprintf("%d", listenPort))
				cbIP = autoIP
			}

			ln, err := net.Listen("tcp", bindAddr)
			if err != nil {
				return err
			}
			defer ln.Close()
			port := ln.Addr().(*net.TCPAddr).Port

			callbackURL := fmt.Sprintf("http://%s:%d/notify", cbIP, port)

			events := make(chan watchEvent, 128)
			var sidToService sync.Map // sid -> service name

			mux := http.NewServeMux()
			mux.HandleFunc("/notify", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "NOTIFY" {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				sid := strings.TrimSpace(r.Header.Get("SID"))
				seq := strings.TrimSpace(r.Header.Get("SEQ"))
				body, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()

				service := "unknown"
				if v, ok := sidToService.Load(sid); ok {
					service = v.(string)
				}

				vars, err := sonos.ParseEvent(body)
				if err != nil {
					vars = map[string]string{"parse_error": err.Error()}
				}

				select {
				case events <- watchEvent{
					Time:    time.Now().UTC(),
					Service: service,
					SID:     sid,
					Seq:     seq,
					Vars:    vars,
				}:
				default:
					// Drop if the consumer is too slow.
				}

				w.WriteHeader(http.StatusOK)
			})

			srv := &http.Server{
				Handler:           mux,
				ReadHeaderTimeout: 5 * time.Second,
			}
			go func() { _ = srv.Serve(ln) }()
			defer func() { _ = srv.Shutdown(context.Background()) }()

			avtSub, err := c.SubscribeAVTransport(ctx, callbackURL, 0)
			if err != nil {
				return err
			}
			defer func() { _ = c.Unsubscribe(context.Background(), avtSub) }()
			sidToService.Store(avtSub.SID, "avtransport")

			rcSub, err := c.SubscribeRenderingControl(ctx, callbackURL, 0)
			if err != nil {
				return err
			}
			defer func() { _ = c.Unsubscribe(context.Background(), rcSub) }()
			sidToService.Store(rcSub.SID, "renderingcontrol")

			// Optional NATS publishing.
			var nc *nats.Conn
			if natsURL != "" {
				nc, err = nats.Connect(natsURL)
				if err != nil {
					return fmt.Errorf("nats connect: %w", err)
				}
				defer nc.Close()
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Connected to NATS at %s (subject prefix: %s)\n", natsURL, natsSubject)
			}

			if !isJSON(flags) && !isTSV(flags) {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Watching events (callback %s). Press Ctrl+C to stop.\n", callbackURL)
			}

			var lastTrack string

			for {
				select {
				case <-ctx.Done():
					if nc != nil {
						_ = nc.Flush()
					}
					return nil
				case ev := <-events:
					// Enrich AVTransport events with track metadata
					// when the track changes or playback starts.
					if ev.Service == "avtransport" {
						track := ev.Vars["current_track"]
						state := ev.Vars["transport_state"]
						if track != lastTrack || state == "PLAYING" {
							if track != "" {
								lastTrack = track
							}
							pos, perr := c.GetPositionInfo(ctx)
							if perr == nil {
								if pos.TrackURI != "" {
									ev.Vars["track_uri"] = pos.TrackURI
								}
								if np, ok := sonos.ParseNowPlaying(pos.TrackMeta); ok {
									if np.Title != "" {
										ev.Vars["title"] = np.Title
									}
									if np.Artist != "" {
										ev.Vars["artist"] = np.Artist
									}
									if np.Album != "" {
										ev.Vars["album"] = np.Album
									}
									if np.AlbumArtURI != "" {
										ev.Vars["album_art_url"] = sonos.AlbumArtURL(c.IP, np.AlbumArtURI)
									}
								}
							}
						}
					}

					// Publish to NATS if connected.
					if nc != nil {
						subject := natsSubject + "." + ev.Service
						data, jerr := json.Marshal(ev)
						if jerr == nil {
							_ = nc.Publish(subject, data)
						}
					}

					if isJSON(flags) {
						_ = writeJSONLine(cmd, ev)
						continue
					}
					if isTSV(flags) {
						keys := make([]string, 0, len(ev.Vars))
						for k := range ev.Vars {
							keys = append(keys, k)
						}
						sort.Strings(keys)
						for _, k := range keys {
							_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", ev.Time.Format(time.RFC3339Nano), ev.Service, ev.SID, k, ev.Vars[k])
						}
						continue
					}

					keys := make([]string, 0, len(ev.Vars))
					for k := range ev.Vars {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					parts := make([]string, 0, len(keys))
					for _, k := range keys {
						parts = append(parts, fmt.Sprintf("%s=%s", k, ev.Vars[k]))
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s [%s] %s\n", ev.Time.Format(time.RFC3339), ev.Service, strings.Join(parts, " "))
				}
			}
		},
	}

	cmd.Flags().DurationVar(&duration, "duration", 0, "Stop after this duration (0 = until Ctrl+C)")
	cmd.Flags().StringVar(&natsURL, "nats-url", "", "NATS server URL (e.g. nats://localhost:4222). If set, events are published to NATS.")
	cmd.Flags().StringVar(&natsSubject, "nats-subject", "sonos.events", "NATS subject prefix for published events")
	cmd.Flags().StringVar(&callbackIP, "callback-ip", "", "External IP for Sonos NOTIFY callbacks (e.g. MetalLB LoadBalancer IP). Overrides auto-detection.")
	cmd.Flags().IntVar(&listenPort, "listen-port", 0, "Fixed port for the callback HTTP server (0 = random)")
	return cmd
}

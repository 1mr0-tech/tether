package cmd

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// demoserver is a hidden command used to run the three demo microservices
// inside the cluster. All three modes are baked into the same binary/image.
//
// Env vars:
//
//	DEMO_MODE     = "frontend" | "backend" | "database-api"
//	DEMO_PORT     = port to listen on
//	DEMO_UPSTREAM = URL of the upstream service (not used by database-api)
var demoServerCmd = &cobra.Command{
	Use:    "demo-server",
	Hidden: true,
	Short:  "Run a demo microservice (frontend | backend | database-api)",
	RunE: func(cmd *cobra.Command, args []string) error {
		mode := os.Getenv("DEMO_MODE")
		port := os.Getenv("DEMO_PORT")
		upstream := os.Getenv("DEMO_UPSTREAM")
		if port == "" {
			port = "8080"
		}

		mux := http.NewServeMux()

		switch mode {
		case "database-api":
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"service":   "database-api",
					"inventory": []string{"widget-A", "widget-B", "widget-C"},
					"count":     3,
					"source":    "cluster",
				})
			})

		case "backend":
			if upstream == "" {
				return fmt.Errorf("DEMO_UPSTREAM must be set for backend mode")
			}
			httpClient := &http.Client{Timeout: 10 * time.Second}
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				resp, err := httpClient.Get(upstream + "/") //nolint:noctx
				if err != nil {
					http.Error(w, `{"error":"upstream unreachable"}`, http.StatusBadGateway)
					return
				}
				defer resp.Body.Close()
				var upstreamData interface{}
				_ = json.NewDecoder(resp.Body).Decode(&upstreamData)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"service":       "backend",
					"processed":     true,
					"upstream_data": upstreamData,
				})
			})

		case "frontend":
			if upstream == "" {
				return fmt.Errorf("DEMO_UPSTREAM must be set for frontend mode")
			}
			httpClient := &http.Client{Timeout: 10 * time.Second}
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				resp, err := httpClient.Get(upstream + "/") //nolint:noctx
				if err != nil {
					http.Error(w, `{"error":"upstream unreachable"}`, http.StatusBadGateway)
					return
				}
				defer resp.Body.Close()
				var backendData interface{}
				_ = json.NewDecoder(resp.Body).Decode(&backendData)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"service":      "frontend",
					"backend_data": backendData,
				})
			})

		default:
			return fmt.Errorf("unknown DEMO_MODE %q — set to frontend, backend, or database-api", mode)
		}

		srv := &http.Server{
			Addr:         ":" + port,
			Handler:      mux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
		}
		log.Printf("demo-server [%s] listening on :%s", mode, port)
		return srv.ListenAndServe()
	},
}

func init() {
	rootCmd.AddCommand(demoServerCmd)
}

// Comando photovault-dedup.
//
// CronJob Kubernetes: prende in carico i job che sa eseguire, li svuota, esce.
// Non cammina l'albero delle cartelle -- prende i file dal database.
package main

import (
	"log/slog"
	"os"
	"strconv"

	"github.com/pste/photovault-dedup/internal/api"
	"github.com/pste/photovault-dedup/internal/dedup"
)

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}

func level(name string) slog.Level {
	switch name {
	case "trace", "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func main() {
	logLevel := level(env("LOG_LEVEL", "info"))
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))

	client := api.New(env("API_URL", "http://localhost:3000"), os.Getenv("API_TOKEN"))
	runner := dedup.New(dedup.Config{
		MediaRoot: env("MEDIA_ROOT", "/data/photos"),
		Batch:     envInt("DEDUP_BATCH", 200),
	}, client, log)

	handlers := map[string]func() (string, error){
		"dedup": runner.Run,
	}

	names := make([]string, 0, len(handlers))
	for name := range handlers {
		names = append(names, name)
	}

	log.Info("pod dedup avviato", "handlers", names)

	executed := 0
	for {
		job, err := client.ClaimJob(names)
		if err != nil {
			log.Error("claim fallito", "err", err)
			os.Exit(1)
		}
		if job == nil {
			break
		}

		log.Info("job preso in carico", "job_id", job.JobID, "name", job.Name)
		result, err := handlers[job.Name]()

		status := "done"
		if err != nil {
			status = "error"
			result = err.Error()
			log.Error("job fallito", "job_id", job.JobID, "err", err)
		} else {
			log.Info("job concluso", "job_id", job.JobID, "result", result)
		}

		if err := client.UpdateJob(job.JobID, status, result); err != nil {
			log.Error("aggiornamento job fallito", "job_id", job.JobID, "err", err)
		}
		executed++
	}

	log.Info("pod dedup concluso", "job_eseguiti", executed)
}

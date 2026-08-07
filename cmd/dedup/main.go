// Comando photovault-dedup.
//
// CronJob Kubernetes: prende in carico i job che sa eseguire, li svuota, esce.
// Non cammina l'albero delle cartelle -- prende i file dal database.
package main

import (
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

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

// envList spezza una variabile "a,b,c" scartando gli spazi e i campi vuoti,
// cosi' che una variabile non valorizzata dia una lista vuota e non [""].
func envList(key string) []string {
	out := []string{}
	for _, part := range strings.Split(os.Getenv(key), ",") {
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, name)
		}
	}
	return out
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

	client := api.New(env("API_URL", "http://localhost:3000"), os.Getenv("API_TOKEN"), log)
	runner := dedup.New(dedup.Config{
		MediaRoot: env("MEDIA_ROOT", "/data/photos"),
		Batch:     envInt("DEDUP_BATCH", 200),
	}, client, log)

	// L'handler riceve il job id: deve mandarne il battito a ogni blocco, o dopo
	// mezz'ora l'API lo considera orfano. Gli sha256 sull'intero archivio
	// durano molto di piu'.
	handlers := map[string]func(int) (string, error){
		"dedup": runner.Run,
		// Solo la parte percettiva: i dHash dalle thumbnail e il rebuild dei
		// gruppi, senza gli sha256 che leggono ogni originale.
		"dhash": runner.RunPerceptual,
	}

	names := make([]string, 0, len(handlers))
	for name := range handlers {
		names = append(names, name)
	}

	log.Info("pod dedup avviato", "handlers", names)

	// Su Kubernetes la schedulazione la fa il CronJob, non questo pod: e' lui
	// che, svegliandosi, mette in coda il lavoro da fare. Senza, il pod
	// troverebbe la coda vuota e uscirebbe subito. L'accodamento e' idempotente
	// (un solo job pending per nome), quindi non fa danni se l'utente lo ha
	// gia' richiesto dalla UI.
	for _, name := range envList("ENQUEUE_ON_START") {
		if _, ok := handlers[name]; !ok {
			log.Error("ENQUEUE_ON_START contiene un job che questo pod non sa eseguire", "name", name)
			os.Exit(1)
		}
		if err := client.EnqueueJob(name, time.Now()); err != nil {
			log.Error("accodamento fallito", "name", name, "err", err)
			os.Exit(1)
		}
		log.Info("job accodato all'avvio", "name", name)
	}

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
		result, err := handlers[job.Name](job.JobID)

		// Un job che non e' piu' nostro non si tocca: scriverci sopra
		// significherebbe raccontare l'esito di un lavoro che sta facendo
		// qualcun altro. Si esce e basta.
		if errors.Is(err, api.ErrJobLost) {
			log.Error("job perso", "job_id", job.JobID, "name", job.Name, "err", err)
			break
		}

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

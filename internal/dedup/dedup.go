// Package dedup svuota le due code di hash e chiede all'API di ricostruire i
// gruppi di duplicati.
package dedup

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pste/photovault-dedup/internal/api"
	"github.com/pste/photovault-dedup/internal/hash"
)

const privateDir = ".photovault"

type Config struct {
	MediaRoot string
	Batch     int
}

type Dedup struct {
	cfg    Config
	client *api.Client
	log    *slog.Logger
}

func New(cfg Config, client *api.Client, log *slog.Logger) *Dedup {
	return &Dedup{cfg: cfg, client: client, log: log}
}

// Le thumbnail sono distribuite su 256 sottocartelle in base al media_id:
// stessa formula usata da photovault-scan quando le genera.
func (d *Dedup) thumbPath(mediaID int, size string) string {
	shard := fmt.Sprintf("%02x", mediaID%256)
	name := strconv.Itoa(mediaID) + "_" + size + ".jpg"
	return filepath.Join(d.cfg.MediaRoot, privateDir, "thumbs", shard, name)
}

func (d *Dedup) originalPath(item api.PendingMedia) string {
	return filepath.Join(d.cfg.MediaRoot, item.RelPath, item.FolderPath, item.FileName)
}

// Run esegue le tre fasi in ordine: sha256, dHash, raggruppamento.
func (d *Dedup) Run() (string, error) {
	hashed, err := d.hashOriginals()
	if err != nil {
		return "", err
	}

	perceptual, err := d.hashThumbnails()
	if err != nil {
		return "", err
	}

	outcome, err := d.client.Rebuild()
	if err != nil {
		return "", fmt.Errorf("rebuild: %w", err)
	}

	return fmt.Sprintf(
		"%d sha256, %d dHash, %d gruppi esatti, %d gruppi simili",
		hashed, perceptual, outcome.GruppiEsatti, outcome.GruppiSimili), nil
}

// hashOriginals legge i file veri: e' il passaggio costoso, perche' obbliga a
// una passata di lettura completa sulla share. E' anche il motivo per cui e'
// incrementale -- solo i media senza hash vengono letti, quindi dopo il primo
// giro costa quasi nulla.
func (d *Dedup) hashOriginals() (int, error) {
	done := 0
	for {
		pending, err := d.client.GetPending("hash", d.cfg.Batch)
		if err != nil {
			return done, fmt.Errorf("coda sha256: %w", err)
		}
		if len(pending) == 0 {
			return done, nil
		}

		results := make([]api.HashResult, 0, len(pending))
		for _, item := range pending {
			path := d.originalPath(item)
			sum, kind, err := hash.FileHash(path)
			if err != nil {
				// Un file illeggibile non deve bloccare il giro. Resta senza
				// hash e verra' ritentato al prossimo run.
				d.log.Warn("sha256 fallito", "media_id", item.MediaID, "file", item.FileName, "err", err)
				continue
			}
			results = append(results, api.HashResult{
				MediaID: item.MediaID, ContentHash: sum, HashKind: kind,
			})
		}

		if len(results) == 0 {
			// Nessun progresso possibile: se si continuasse, l'API
			// restituirebbe all'infinito gli stessi file illeggibili.
			d.log.Warn("nessun file leggibile nel blocco: interrompo la fase sha256")
			return done, nil
		}

		if err := d.client.SendHashes(results); err != nil {
			return done, fmt.Errorf("invio sha256: %w", err)
		}
		done += len(results)
		d.log.Info("sha256 calcolati", "totale", done)
	}
}

// hashThumbnails lavora sulle thumbnail 's': ~15 KB invece di 6 MB, e sono
// gia' ridimensionate.
func (d *Dedup) hashThumbnails() (int, error) {
	done := 0
	for {
		pending, err := d.client.GetPending("dhash", d.cfg.Batch)
		if err != nil {
			return done, fmt.Errorf("coda dHash: %w", err)
		}
		if len(pending) == 0 {
			return done, nil
		}

		results := make([]api.HashResult, 0, len(pending))
		for _, item := range pending {
			path := d.thumbPath(item.MediaID, "s")
			if _, err := os.Stat(path); err != nil {
				d.log.Debug("thumbnail assente", "media_id", item.MediaID)
				continue
			}
			bits, err := hash.DHash(path)
			if err != nil {
				d.log.Warn("dHash fallito", "media_id", item.MediaID, "err", err)
				continue
			}
			results = append(results, api.HashResult{MediaID: item.MediaID, DHash: bits})
		}

		if len(results) == 0 {
			d.log.Warn("nessuna thumbnail utilizzabile nel blocco: interrompo la fase dHash")
			return done, nil
		}

		if err := d.client.SendHashes(results); err != nil {
			return done, fmt.Errorf("invio dHash: %w", err)
		}
		done += len(results)
		d.log.Info("dHash calcolati", "totale", done)
	}
}

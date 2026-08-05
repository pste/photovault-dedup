// Package api parla con photovault-api. Questo pod non conosce PostgreSQL:
// chiede cosa manca e rimanda indietro i risultati, tutto via /api/internal.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		// Il rebuild dei gruppi confronta l'intero corpus al primo giro: puo'
		// richiedere minuti su un archivio grande.
		http: &http.Client{Timeout: 300 * time.Second},
	}
}

type Job struct {
	JobID int    `json:"job_id"`
	Name  string `json:"name"`
}

// PendingMedia e' un media a cui manca un hash. FolderPath e RelPath servono a
// ricostruire il percorso dell'originale; per il dHash si usa la thumbnail,
// che si ricava dal solo MediaID.
type PendingMedia struct {
	MediaID    int    `json:"media_id"`
	FileName   string `json:"file_name"`
	MediaKind  string `json:"media_kind"`
	Ext        string `json:"ext"`
	FolderPath string `json:"folder_path"`
	RelPath    string `json:"rel_path"`
}

type HashResult struct {
	MediaID     int    `json:"media_id"`
	ContentHash string `json:"content_hash,omitempty"`
	HashKind    string `json:"hash_kind,omitempty"`
	DHash       string `json:"dhash,omitempty"`
}

type RebuildOutcome struct {
	GruppiEsatti  int `json:"gruppi_esatti"`
	GruppiSimili  int `json:"gruppi_simili"`
	Confrontati   int `json:"confrontati"`
	GruppiRimossi int `json:"gruppi_rimossi"`
}

func (c *Client) do(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("%s %s: %s: %s", method, path, res.Status, string(payload))
	}
	if out != nil && len(payload) > 0 {
		return json.Unmarshal(payload, out)
	}
	return nil
}

// ClaimJob dichiara quali job questo pod sa eseguire: senza l'elenco si
// prenderebbe anche quelli degli altri pod e li marcherebbe in errore.
func (c *Client) ClaimJob(names []string) (*Job, error) {
	var job *Job
	err := c.do("POST", "/api/internal/jobs/claim", map[string]any{"names": names}, &job)
	return job, err
}

func (c *Client) UpdateJob(jobID int, status, result string) error {
	body := map[string]any{"status": status, "result": result}
	return c.do("PATCH", fmt.Sprintf("/api/internal/jobs/%d", jobID), body, nil)
}

// EnqueueJob mette in coda un job. L'API tiene un solo pending per nome,
// quindi rilanciarlo non accumula lavoro doppio.
func (c *Client) EnqueueJob(name string, when time.Time) error {
	body := map[string]any{"name": name, "when": when.UTC().Format(time.RFC3339)}
	return c.do("POST", "/api/internal/jobs", body, nil)
}

// GetPending: stage "hash" per i media senza sha256, "dhash" per quelli con
// thumbnail pronta ma senza hash percettivo.
func (c *Client) GetPending(stage string, limit int) ([]PendingMedia, error) {
	var out []PendingMedia
	err := c.do("GET", fmt.Sprintf("/api/internal/pending/%s?limit=%d", stage, limit), nil, &out)
	return out, err
}

func (c *Client) SendHashes(items []HashResult) error {
	return c.do("POST", "/api/internal/dedup/hashes", map[string]any{"items": items}, nil)
}

func (c *Client) Rebuild() (*RebuildOutcome, error) {
	var out RebuildOutcome
	err := c.do("POST", "/api/internal/dedup/rebuild", nil, &out)
	return &out, err
}

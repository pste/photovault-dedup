// Package api parla con photovault-api. Questo pod non conosce PostgreSQL:
// chiede cosa manca e rimanda indietro i risultati, tutto via /api/internal.
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	log     *slog.Logger
}

func New(baseURL, token string, log *slog.Logger) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		log:     log,
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

// ErrJobLost dice che l'API non riconosce piu' questo pod come titolare del
// job: qualcuno lo ha chiuso a mano, o il reaper lo ha gia' recuperato.
// Chi lo riceve deve fermarsi: il lavoro fatto e' gia' salvato, e continuare
// significa duplicare quello di un altro pod.
var ErrJobLost = errors.New("job non piu' in carico a questo pod")

func (c *Client) do(method, path string, body any, out any) error {
	_, err := c.doStatus(method, path, body, out)
	return err
}

// doStatus e' do() che riporta anche il codice HTTP: serve al battito, che deve
// distinguere il 409 da un errore di rete.
func (c *Client) doStatus(method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return res.StatusCode, err
	}
	if res.StatusCode >= 400 {
		return res.StatusCode, fmt.Errorf("%s %s: %s: %s", method, path, res.Status, string(payload))
	}
	if out != nil && len(payload) > 0 {
		return res.StatusCode, json.Unmarshal(payload, out)
	}
	return res.StatusCode, nil
}

// ClaimJob dichiara quali job questo pod sa eseguire: senza l'elenco si
// prenderebbe anche quelli degli altri pod e li marcherebbe in errore.
func (c *Client) ClaimJob(names []string) (*Job, error) {
	var job *Job
	err := c.do("POST", "/api/internal/jobs/claim", map[string]any{"names": names}, &job)
	return job, err
}

// Heartbeat dice all'API che questo pod e' vivo e sta ancora lavorando sul job.
// Senza, dopo mezz'ora di silenzio il claim lo considera orfano e lo recupera:
// il calcolo degli sha256 sull'intero archivio dura molto piu' di mezz'ora.
//
// Un errore qui non e' fatale: il lavoro fatto e' gia' salvato, e la coda in
// database resta la fonte di verita'.
// Restituisce ErrJobLost se l'API risponde 409. Un errore di rete invece non
// ferma niente: il lavoro e' gia' salvato e il giro dopo si riprende dalla coda.
func (c *Client) Heartbeat(jobID int) error {
	if jobID <= 0 {
		return nil
	}
	status, err := c.doStatus("POST", fmt.Sprintf("/api/internal/jobs/%d/heartbeat", jobID), nil, nil)
	if status == http.StatusConflict {
		c.log.Warn("battito rifiutato: il job non e' piu' nostro", "job_id", jobID)
		return ErrJobLost
	}
	if err != nil {
		c.log.Warn("battito non riuscito", "job_id", jobID, "err", err)
	}
	return nil
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

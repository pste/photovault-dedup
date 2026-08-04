// Package hash calcola i due hash su cui si regge la ricerca dei duplicati.
package hash

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"os"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// Sopra questa soglia si usa lo sha256 parziale: leggere per intero un video da
// 4 GB su SMB costa una quarantina di secondi, e per i file da fotocamera i
// primi e ultimi 16 MB piu' la dimensione sono in pratica gia' univoci.
const partialThreshold = 256 << 20 // 256 MB
const partialChunk = 16 << 20      // 16 MB

// FileHash calcola lo sha256 del contenuto, restituendo anche quale variante e'
// stata usata: il tipo va salvato accanto all'hash, altrimenti due file
// confrontati con criteri diversi sembrerebbero diversi senza motivo.
func FileHash(path string) (sum string, kind string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", "", err
	}

	digest := sha256.New()

	if info.Size() <= partialThreshold {
		if _, err := io.Copy(digest, file); err != nil {
			return "", "", err
		}
		return hex.EncodeToString(digest.Sum(nil)), "sha256", nil
	}

	head := make([]byte, partialChunk)
	if _, err := io.ReadFull(file, head); err != nil {
		return "", "", err
	}
	digest.Write(head)

	if _, err := file.Seek(-partialChunk, io.SeekEnd); err != nil {
		return "", "", err
	}
	tail := make([]byte, partialChunk)
	if _, err := io.ReadFull(file, tail); err != nil {
		return "", "", err
	}
	digest.Write(tail)

	// La dimensione entra nell'hash: due video diversi con testa e coda uguali
	// (tagli dello stesso girato) restano distinti.
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(info.Size()))
	digest.Write(size[:])

	return hex.EncodeToString(digest.Sum(nil)), "sha256-partial", nil
}

// DHash calcola l'hash percettivo a 64 bit di un'immagine.
//
// Si ridimensiona a 9x8 in scala di grigi e si confrontano i pixel
// orizzontalmente adiacenti: ogni confronto e' un bit. E' robusto proprio dove
// serve -- la stessa foto riesportata a dimensione e qualita' diverse -- e
// costa una dozzina di righe invece di una DCT.
//
// Si lavora sulla thumbnail, non sull'originale: sono ~15 KB invece di 6 MB,
// ed e' gia' ridimensionata.
func DHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return "", err
	}

	const w, h = 9, 8
	gray := resizeGray(img, w, h)

	var bits uint64
	pos := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w-1; x++ {
			if gray[y*w+x] > gray[y*w+x+1] {
				bits |= 1 << (63 - pos)
			}
			pos++
		}
	}

	// PostgreSQL vuole una stringa di 64 caratteri '0'/'1' per il tipo bit(64).
	return fmt.Sprintf("%064b", bits), nil
}

// resizeGray riduce a w x h in scala di grigi campionando a box: per una
// griglia cosi' piccola e' sufficiente, e non serve trascinarsi dietro un
// ridimensionatore di qualita'.
func resizeGray(img image.Image, w, h int) []float64 {
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	out := make([]float64, w*h)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			x0 := bounds.Min.X + x*srcW/w
			x1 := bounds.Min.X + (x+1)*srcW/w
			y0 := bounds.Min.Y + y*srcH/h
			y1 := bounds.Min.Y + (y+1)*srcH/h
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if y1 <= y0 {
				y1 = y0 + 1
			}

			var sum float64
			var n float64
			for sy := y0; sy < y1 && sy < bounds.Max.Y; sy++ {
				for sx := x0; sx < x1 && sx < bounds.Max.X; sx++ {
					r, g, b, a := img.At(sx, sy).RGBA()

					// RGBA() restituisce valori PREMOLTIPLICATI per l'alpha:
					// un pixel completamente trasparente e' (0,0,0,0), cioe'
					// nero. Senza compositare, ogni PNG con sfondo trasparente
					// diventa "un'immagine nera con una macchia" e finisce per
					// somigliare a tutte le altre.
					// Si composita su bianco, che e' come le si guarda davvero.
					inv := 65535 - a
					r += inv
					g += inv
					b += inv

					// Luminanza percettiva: il verde pesa piu' del blu.
					sum += 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
					n++
				}
			}
			if n > 0 {
				out[y*w+x] = sum / n
			}
		}
	}
	return out
}

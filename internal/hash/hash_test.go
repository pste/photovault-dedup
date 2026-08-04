package hash

import (
	"math/bits"
	"strconv"
	"testing"
)

// hamming conta i bit diversi tra due hash. E' lo stesso calcolo che fa
// PostgreSQL con bit_count(a # b), qui riprodotto per verificare che i valori
// prodotti in Go abbiano davvero il significato atteso.
func hamming(t *testing.T, a, b string) int {
	t.Helper()
	if len(a) != 64 || len(b) != 64 {
		t.Fatalf("un dHash deve essere lungo 64 caratteri, non %d e %d", len(a), len(b))
	}
	ua, err := strconv.ParseUint(a, 2, 64)
	if err != nil {
		t.Fatalf("hash non binario: %v", err)
	}
	ub, err := strconv.ParseUint(b, 2, 64)
	if err != nil {
		t.Fatalf("hash non binario: %v", err)
	}
	return bits.OnesCount64(ua ^ ub)
}

func mustDHash(t *testing.T, path string) string {
	t.Helper()
	out, err := DHash(path)
	if err != nil {
		t.Fatalf("DHash(%s): %v", path, err)
	}
	return out
}

// Lo stesso file deve dare sempre lo stesso hash: senza determinismo il
// confronto incrementale non avrebbe senso.
func TestDHashDeterministico(t *testing.T) {
	a := mustDHash(t, "testdata/original.jpg")
	b := mustDHash(t, "testdata/original.jpg")
	if a != b {
		t.Errorf("due letture dello stesso file danno hash diversi:\n%s\n%s", a, b)
	}
}

// Il caso per cui esiste il dHash: la stessa foto riesportata piu' piccola e
// piu' compressa (tipico di WhatsApp) deve restare sotto la soglia di default.
func TestDHashRiconosceLaStessaFotoRiesportata(t *testing.T) {
	const sogliaDefault = 6

	original := mustDHash(t, "testdata/original.jpg")
	resized := mustDHash(t, "testdata/resized.jpg")

	distanza := hamming(t, original, resized)
	if distanza > sogliaDefault {
		t.Errorf("distanza = %d, deve stare entro la soglia di %d", distanza, sogliaDefault)
	}
	t.Logf("stessa foto riesportata: distanza %d", distanza)
}

// Due foto diverse devono stare ben oltre la soglia, altrimenti la pagina
// Duplicati si riempirebbe di falsi positivi.
func TestDHashDistingueFotoDiverse(t *testing.T) {
	const sogliaDefault = 6

	original := mustDHash(t, "testdata/original.jpg")
	different := mustDHash(t, "testdata/different.jpg")

	distanza := hamming(t, original, different)
	if distanza <= sogliaDefault {
		t.Errorf("distanza = %d: due foto diverse non devono somigliarsi", distanza)
	}
	t.Logf("foto diverse: distanza %d", distanza)
}

// Un'immagine piatta produce un hash degenere (quasi tutti zeri o tutti uni).
// Somiglierebbe a qualunque altra immagine piatta, creando un unico enorme
// gruppo di falsi positivi: per questo la query di raggruppamento la scarta con
// bit_count(dhash) BETWEEN 8 AND 56. Questo test documenta il perche'.
func TestDHashImmaginePiattaEDegenere(t *testing.T) {
	flat := mustDHash(t, "testdata/flat.jpg")
	value, err := strconv.ParseUint(flat, 2, 64)
	if err != nil {
		t.Fatalf("hash non binario: %v", err)
	}
	popcount := bits.OnesCount64(value)

	if popcount >= 8 && popcount <= 56 {
		t.Errorf("popcount = %d: l'immagine piatta dovrebbe cadere fuori dall'intervallo 8..56", popcount)
	}
	t.Logf("immagine piatta: popcount %d, correttamente scartata dal filtro", popcount)
}

// Lo sha256 deve distinguere file diversi e riconoscere quelli identici.
func TestFileHash(t *testing.T) {
	a, kindA, err := FileHash("testdata/original.jpg")
	if err != nil {
		t.Fatalf("FileHash: %v", err)
	}
	if kindA != "sha256" {
		t.Errorf("tipo = %q, atteso sha256 per un file piccolo", kindA)
	}
	if len(a) != 64 {
		t.Errorf("lo sha256 esadecimale deve essere lungo 64, non %d", len(a))
	}

	b, _, err := FileHash("testdata/different.jpg")
	if err != nil {
		t.Fatalf("FileHash: %v", err)
	}
	if a == b {
		t.Error("due file diversi hanno lo stesso sha256")
	}

	again, _, err := FileHash("testdata/original.jpg")
	if err != nil {
		t.Fatalf("FileHash: %v", err)
	}
	if a != again {
		t.Error("due letture dello stesso file danno sha256 diversi")
	}
}

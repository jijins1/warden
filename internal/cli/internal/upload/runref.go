package upload

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// crockford is base32 as Crockford defines it: no I, L, O or U.
//
// Those four are excluded so a transcribed identifier cannot be confused with 1
// and 0 — which matters here because a run reference ends up in error messages,
// in support tickets, and read aloud over a call.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewRunRef mints the public identifier of a run: a 26-character ULID.
//
// The CLI mints it rather than asking the server, and that is the whole reason
// there is no reservation round trip (ADR-0028). The server does not trust it —
// it validates the shape before composing anything with it, because the value
// ends up in a git ref name and in a URL.
//
// Collisions are not a correctness concern here even though the client picks the
// value: the ref it names lives under the caller's own audit namespace, and an
// audit ref is written once (ADR-0027). Two runs colliding means the second push
// is refused, loudly, rather than one overwriting the other.
func NewRunRef() (string, error) {
	var raw [16]byte

	// 48 bits of millisecond timestamp, big-endian, as ULID specifies. It makes
	// identifiers sort chronologically, which is what makes a listing of refs
	// readable without joining anything.
	// Le masquage n'est pas cosmétique : il DIT que la troncature à 48 bits est
	// la spécification et non un accident. ULID garde 48 bits de millisecondes,
	// ce qui va jusqu'en 10889 — le débordement n'est pas un cas à traiter, c'est
	// un cas qui n'arrivera pas.
	ms := uint64(time.Now().UnixMilli()) & ((1 << 48) - 1)
	binary.BigEndian.PutUint16(raw[0:2], uint16((ms>>32)&0xFFFF))
	binary.BigEndian.PutUint32(raw[2:6], uint32(ms&0xFFFFFFFF))

	// 80 bits of randomness. crypto/rand and not math/rand: this identifier is
	// part of a ref name that bounds what a push may write, and a predictable
	// suffix would let one user guess another's refs — which ADR-0026 established
	// is the prerequisite of every attack it closed.
	if _, err := rand.Read(raw[6:]); err != nil {
		return "", fmt.Errorf("drawing a run identifier: %w", err)
	}

	return encodeCrockford(raw), nil
}

// encodeCrockford renders 128 bits as 26 base32 characters.
//
// 26 characters hold 130 bits, so the first one carries only the top 2 bits and
// the alphabet index can never exceed 7 there. Written as a plain bit walk rather
// than the usual hand-unrolled ULID encoder: this runs once per run, and the
// unrolled version is a page of magic constants nobody re-reads.
func encodeCrockford(raw [16]byte) string {
	out := make([]byte, 26)
	for i := range out {
		// Bit offset of this character's 5 bits, counted from the left of the
		// 130-bit field, of which the first 2 bits are padding.
		start := i*5 - 2

		var v int
		for b := range 5 {
			pos := start + b
			if pos < 0 {
				continue // padding ahead of the value
			}
			bit := (raw[pos/8] >> (7 - pos%8)) & 1
			v = v<<1 | int(bit)
		}
		out[i] = crockford[v]
	}
	return string(out)
}

// RunRefTime rend l'instant encodé dans un identifiant de run.
//
// Il existe pour une raison précise : le commit d'audit doit être DÉTERMINISTE.
// `CommitTree` reçoit toutes les valeurs qui le composent, dates comprises, pour
// que le même arbre sur la même base produise toujours le même SHA — c'est ce qui
// rend un push rejoué idempotent au lieu de créer un second commit disant la même
// chose (ADR-0025).
//
// Un `time.Now()` à cet endroit casserait cette propriété. Une date fixe la
// tiendrait, mais daterait tous les commits d'audit de 1970, ce qui rend un
// `git log` sur les refs d'audit inutilisable. Reprendre l'horodatage du ULID
// donne les deux : déterministe pour un identifiant donné, et juste.
func RunRefTime(ref string) (time.Time, error) {
	if len(ref) != 26 {
		return time.Time{}, fmt.Errorf("identifiant de run de longueur %d, attendu 26", len(ref))
	}

	// Les 10 premiers caractères portent 50 bits, dont les 2 de tête sont du
	// remplissage : il reste les 48 bits de millisecondes.
	var ms uint64
	for i := range 10 {
		v := strings.IndexByte(crockford, ref[i])
		if v < 0 {
			return time.Time{}, fmt.Errorf("caractère %q hors de l'alphabet", ref[i])
		}
		ms = ms<<5 | uint64(v)
	}
	ms &= (1 << 48) - 1

	return time.UnixMilli(int64(ms)).UTC(), nil
}

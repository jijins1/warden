package gitd

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// authentificateur porte l'empreinte du jeton attendu sur l'API interne.
//
// Le jeton est partagé et statique : le rôle `api` et le rôle `git` le lisent
// dans la même variable d'environnement. C'est le strict nécessaire pour que
// l'arête ne soit pas ouverte, pas une identité — il n'a ni expiration, ni
// audience, ni porteur nommé. L'ADR-0024 le dit et désigne mTLS comme la cible.
type authentificateur struct{ empreinte [32]byte }

func nouvelAuth(jeton string) authentificateur {
	return authentificateur{empreinte: sha256.Sum256([]byte(jeton))}
}

// middleware refuse sans rien apprendre à l'appelant.
//
// Ni corps, ni en-tête WWW-Authenticate, ni distinction entre « jeton absent »
// et « jeton faux » : le rôle `git` est joignable depuis les pods runner, donc
// depuis du code arbitraire (ADR-0007), et chacune de ces différences est une
// information offerte à qui sonde.
func (a authentificateur) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefixe = "Bearer "
		brut := r.Header.Get("Authorization")
		if len(brut) <= len(prefixe) || !strings.EqualFold(brut[:len(prefixe)], prefixe) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Comparaison sur les EMPREINTES et non sur les octets du jeton : elles
		// ont une longueur fixe, donc la durée de la comparaison ne dépend ni de
		// la valeur présentée ni de sa taille. L'ADR-0023 pose déjà qu'aucun
		// secret ne se compare octet à octet.
		presentee := sha256.Sum256([]byte(strings.TrimSpace(brut[len(prefixe):])))
		if subtle.ConstantTimeCompare(presentee[:], a.empreinte[:]) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

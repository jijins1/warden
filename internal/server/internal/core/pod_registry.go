package core

// NewPod décrit un pod chaud au moment où le contrôleur l'enregistre.
//
// L'enregistrement précède la création du pod : le Secret qui porte le jeton
// doit exister avant que le kubelet monte le volume, faute de quoi le pod attend
// au montage — et l'attente au démarrage est ce que le pré-chauffage supprime
// (§8.1).
type NewPod struct {
	PodName string
	Pool    PodPool
	// Token n'est pas stocké : seule son empreinte l'est (ADR-0023).
	Token PodToken

	// RunID attache le pod à un run dès l'enregistrement.
	//
	// Zéro pour un pod de POOL, qui naît libre et sera réclamé plus tard. Non nul
	// pour un pod créé À LA DEMANDE, qui n'a jamais été destiné à un autre run :
	// le faire naître libre pour le réclamer aussitôt ferait passer par le
	// `ready_at IS NOT NULL` de ClaimPod, donc attendre que le pod se connecte
	// avant de pouvoir lui attribuer le run pour lequel il vient d'être créé.
	//
	// Le pré-chauffage reviendra remplir le cas zéro. C'est une optimisation, pas
	// une fonctionnalité : sans lui le produit marche et démarre plus lentement.
	RunID int64
}

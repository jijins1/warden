// Package enroll pose la clé publique d'un environnement sur une machine.
//
// C'est le MODE ASSISTÉ de l'enrôlement : l'utilisateur donne un login et un mot
// de passe, warden ouvre la session et installe la clé. Le mode déclaratif, lui,
// ne passe pas par ici — l'opérateur y a posé la clé lui-même.
//
// # Pourquoi depuis le pod
//
// Trois raisons, la dernière étant décisive. Le canal de dispatch est le seul
// conçu pour porter des credentials (ADR-0018). Donner au control plane un
// egress SSH vers des adresses arbitraires en ferait un pivot. Et surtout :
// c'est depuis le pod que la joignabilité COMPTE — un enrôlement vérifié depuis
// `wardend` peut parfaitement échouer au premier run.
//
// # Pourquoi pas le module Ansible
//
// `ansible.posix.authorized_key` exige Python sur la cible. Une machine fraîche
// n'en a pas forcément, et c'est précisément le cas qu'un enrôlement doit
// couvrir. Un `exec` SSH brut marche partout.
package enroll

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Delais bornant une session d'enrôlement.
//
// Courts, et ce n'est pas de la frilosité : un enrôlement qui traîne est un pod
// qui tient un mot de passe en mémoire. Une machine qui ne répond pas en dix
// secondes ne répondra pas mieux en soixante.
const (
	DelaiConnexion = 15 * time.Second
	DelaiSession   = 30 * time.Second
)

// ErrEmpreinteChangee signale que la machine n'est pas celle qu'on attendait.
//
// C'est le cas de l'intermédiaire, et il est distingué de tout le reste : le
// confondre avec une panne réseau serait la pire confusion possible du produit,
// parce que la conduite à tenir est l'inverse — on ne réessaie pas, on enquête.
var ErrEmpreinteChangee = errors.New("l'empreinte de la machine ne correspond pas à celle attendue")

// ErrAuthentification signale un login ou un mot de passe refusé.
var ErrAuthentification = errors.New("la machine a refusé le login et le mot de passe")

// Cible décrit la machine à enrôler.
type Cible struct {
	Host    string
	Address string
	Port    int
	Login   string

	// Password est éphémère : il vient de l'utilisateur, transite par le canal
	// de dispatch, et n'est écrit nulle part.
	Password []byte

	// PublicKey est la ligne à poser dans `authorized_keys`.
	PublicKey string

	// HostKey est l'empreinte ATTENDUE, vide au premier contact.
	HostKey string
}

// Resultat est ce qu'un enrôlement établit.
type Resultat struct {
	// HostKey est l'empreinte CONSTATÉE, au format d'une ligne `known_hosts`
	// sans le nom d'hôte. C'est elle qu'on épingle ensuite.
	HostKey string

	// Fingerprint est la même chose sous la forme qu'un humain compare.
	Fingerprint string

	// DejaPresente dit que la clé était déjà dans l'`authorized_keys`. Ce n'est
	// pas un échec — c'est le cas d'un réenrôlement — mais ça change ce qu'on
	// raconte à l'utilisateur.
	DejaPresente bool
}

// Journal reçoit le récit des étapes.
//
// Une fonction et non un logger : ces lignes deviennent des events de run, donc
// de l'audit, et pas des traces de processus.
type Journal func(etape string)

// Enroler ouvre la session, pose la clé, et VÉRIFIE qu'elle ouvre.
//
// La vérification finale n'est pas une précaution : sans elle, on déclarerait
// enrôlée une machine qu'aucun run ne pourra joindre — et on le découvrirait au
// pire moment, sur un playbook de production.
func Enroler(ctx context.Context, c Cible, dire Journal) (Resultat, error) {
	if dire == nil {
		dire = func(string) {}
	}
	if err := c.valider(); err != nil {
		return Resultat{}, err
	}

	adresse := net.JoinHostPort(c.Address, strconv.Itoa(c.Port))

	// Première session : par MOT DE PASSE. C'est celle qui capture l'empreinte.
	dire(fmt.Sprintf("connexion à %s en tant que %s", adresse, c.Login))
	vue := &empreinteVue{attendue: c.HostKey}

	client, err := composer(ctx, adresse, ssh.ClientConfig{
		User:            c.Login,
		Auth:            []ssh.AuthMethod{ssh.Password(string(c.Password))},
		HostKeyCallback: vue.verifier,
		Timeout:         DelaiConnexion,
	})
	if err != nil {
		// L'erreur d'empreinte est remontée telle quelle : elle a sa propre
		// classe, et l'enrobage la rendrait indiscernable d'un refus ordinaire.
		if errors.Is(err, ErrEmpreinteChangee) {
			return Resultat{}, err
		}
		if estRefusDAuth(err) {
			return Resultat{}, fmt.Errorf("%w (%s@%s)", ErrAuthentification, c.Login, adresse)
		}
		return Resultat{}, fmt.Errorf("connexion à %s : %w", adresse, err)
	}
	defer func() { _ = client.Close() }()

	res := Resultat{
		HostKey:     vue.ligne,
		Fingerprint: vue.empreinte,
	}
	dire("empreinte de la machine : " + res.Fingerprint)

	deja, err := poser(ctx, client, c.PublicKey)
	if err != nil {
		return res, err
	}
	res.DejaPresente = deja
	if deja {
		dire("la clé était déjà dans authorized_keys")
	} else {
		dire("clé ajoutée à authorized_keys")
	}

	// Seconde session : par LA CLÉ. C'est elle qui transforme « on a écrit un
	// fichier » en « la machine est joignable ».
	dire("vérification : nouvelle connexion avec la clé")
	if err := verifier(ctx, adresse, c, vue.ligne); err != nil {
		return res, err
	}
	dire("enrôlement vérifié")
	return res, nil
}

func (c Cible) valider() error {
	switch {
	case c.Address == "":
		return errors.New("enrôlement : adresse absente")
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("enrôlement : port %d hors bornes", c.Port)
	case c.Login == "":
		return errors.New("enrôlement : compte absent")
	case len(c.Password) == 0:
		return errors.New("enrôlement : mot de passe absent")
	case c.PublicKey == "":
		return errors.New("enrôlement : aucune clé publique à poser")
	}
	// La clé qu'on s'apprête à écrire dans un `authorized_keys` doit en être
	// une. Sans ce contrôle, une chaîne quelconque partirait dans le fichier et
	// le rendrait peut-être illisible pour les lignes suivantes.
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.PublicKey)); err != nil {
		return fmt.Errorf("enrôlement : la clé publique est illisible : %w", err)
	}
	return nil
}

// empreinteVue capture la clé d'hôte et applique l'épinglage.
type empreinteVue struct {
	attendue  string
	ligne     string
	empreinte string
}

// verifier est le `HostKeyCallback` de la session.
//
// C'est LE point de sécurité de ce paquet. Deux régimes :
//
//   - empreinte attendue fournie → tout écart fait ÉCHOUER. C'est le cas de
//     l'intermédiaire.
//   - empreinte absente → on accepte et on CAPTURE. C'est la confiance à la
//     première rencontre, et elle n'est pas protégée : la seule parade est de
//     déclarer l'empreinte à l'avance. L'appelant doit le dire à l'utilisateur.
func (e *empreinteVue) verifier(_ string, _ net.Addr, cle ssh.PublicKey) error {
	e.ligne = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(cle)))
	e.empreinte = ssh.FingerprintSHA256(cle)

	if e.attendue == "" {
		return nil
	}
	// Comparaison sur les OCTETS de la clé et non sur la chaîne : une même clé
	// peut s'écrire avec un commentaire différent, et comparer les textes ferait
	// échouer un enrôlement parfaitement légitime.
	attendue, _, _, _, err := ssh.ParseAuthorizedKey([]byte(e.attendue))
	if err != nil {
		return fmt.Errorf("empreinte attendue illisible : %w", err)
	}
	if !bytes.Equal(attendue.Marshal(), cle.Marshal()) {
		return fmt.Errorf("%w (constatée %s)", ErrEmpreinteChangee, e.empreinte)
	}
	return nil
}

// composer ouvre la session en respectant l'annulation.
//
// `ssh.Dial` ne prend pas de contexte, et le problème ne s'arrête pas là : la
// poignée de main SSH n'est PAS interruptible non plus. Une machine qui accepte
// la connexion TCP puis ne dit plus rien — le noyau accepte pour elle, via le
// backlog — retiendrait donc l'enrôlement jusqu'au délai complet, Ctrl-C
// compris. MESURÉ : le test d'annulation attendait les quinze secondes.
//
// La connexion est donc FERMÉE quand le contexte tombe, ce qui fait échouer la
// poignée de main en cours. C'est la seule façon de l'interrompre.
func composer(ctx context.Context, adresse string, cfg ssh.ClientConfig) (*ssh.Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", adresse)
	if err != nil {
		return nil, err
	}

	// Le délai borne la poignée de main d'une machine simplement lente ;
	// l'annulation, elle, passe par la fermeture ci-dessous.
	if err := conn.SetDeadline(time.Now().Add(DelaiConnexion)); err != nil {
		_ = conn.Close()
		return nil, err
	}

	negocie := make(chan struct{})
	defer close(negocie)
	go func() {
		select {
		case <-negocie:
		case <-ctx.Done():
			// Fermer sous les pieds de la poignée de main la fait échouer, ce
			// qui est exactement l'effet voulu : rendre la main.
			_ = conn.Close()
		}
	}()

	c, chans, reqs, err := ssh.NewClientConn(conn, adresse, &cfg)
	if err != nil {
		_ = conn.Close()
		// Une poignée de main coupée par l'annulation ressemble à une panne
		// réseau. Dire laquelle des deux c'était évite de faire chercher un
		// problème de machine là où il n'y en a pas.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("connexion interrompue : %w", ctx.Err())
		}
		return nil, err
	}
	// L'échéance est levée : elle couperait les sessions ouvertes ensuite.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = c.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// CodeDejaPresente est le code de sortie par lequel la machine dit que la clé
// était déjà là.
//
// Un CODE DE SORTIE et non un marqueur dans la sortie standard, et c'est une
// correction : gratter le stdout d'un shell distant pour en déduire un état est
// fragile par nature, et ça l'a été — le marqueur se perdait au gré de la
// fermeture du canal, ce qui rendait le résultat instable une fois sur cinq. Le
// code de sortie voyage dans une requête SSH dédiée, que `Session.Run` remonte
// comme un `*ssh.ExitError`.
//
// 3 plutôt qu'un code voisin de 0 ou 1 : ceux-là sont ceux qu'un shell rend
// tout seul quand quelque chose ne va pas, et on ne veut pas confondre « déjà
// présente » avec « le script a échoué ».
const CodeDejaPresente = 3

// commandePose ajoute la clé de façon IDEMPOTENTE.
//
// `grep -qxF` compare des lignes ENTIÈRES et littéralement : sans `-x`, une clé
// dont une autre est le préfixe passerait pour présente ; sans `-F`, le contenu
// serait lu comme une expression rationnelle.
//
// Le `umask 077` et les `chmod` ne sont pas décoratifs : `sshd` REFUSE un
// `authorized_keys` accessible en écriture au groupe, et le refus est silencieux
// côté client — la machine répondrait « permission denied » sans dire pourquoi.
const commandePose = `set -e
umask 077
mkdir -p ~/.ssh
chmod 700 ~/.ssh
touch ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
if grep -qxF "$WARDEN_KEY" ~/.ssh/authorized_keys 2>/dev/null; then
  exit 3
fi
printf '%s\n' "$WARDEN_KEY" >> ~/.ssh/authorized_keys`

// poser installe la clé et dit si elle y était déjà.
func poser(ctx context.Context, client *ssh.Client, publique string) (bool, error) {
	session, err := client.NewSession()
	if err != nil {
		return false, fmt.Errorf("ouverture de session : %w", err)
	}
	defer func() { _ = session.Close() }()

	// La clé voyage par une VARIABLE et non concaténée dans le script : elle
	// vient du serveur, mais l'interpoler dans un shell distant ferait de sa
	// valeur du code. `Setenv` échoue si sshd n'accepte pas la variable, d'où
	// le repli sur `stdin`.
	var sortie bytes.Buffer
	session.Stdout = &sortie
	session.Stderr = &sortie

	// `AcceptEnv` est restrictif chez presque tout le monde : la clé passe donc
	// par l'entrée standard, que rien ne filtre, et le script la lit.
	session.Stdin = strings.NewReader(publique + "\n")
	script := "IFS= read -r WARDEN_KEY\nexport WARDEN_KEY\n" + commandePose

	fini := make(chan error, 1)
	go func() { fini <- session.Run(script) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGINT)
		return false, ctx.Err()

	case err := <-fini:
		if err == nil {
			return false, nil
		}
		// Le code convenu n'est pas une erreur : c'est la réponse « elle y
		// était déjà ». Tout autre code en est une.
		var sortieErr *ssh.ExitError
		if errors.As(err, &sortieErr) && sortieErr.ExitStatus() == CodeDejaPresente {
			return true, nil
		}
		return false, fmt.Errorf("pose de la clé : %w (%s)", err, resume(sortie.String()))

	case <-time.After(DelaiSession):
		return false, errors.New("pose de la clé : la machine ne répond plus")
	}
}

// verifier rouvre une session AVEC LA CLÉ.
//
// L'enrôlement ne se déclare pas réussi sur le fait d'avoir écrit un fichier :
// un `authorized_keys` peut être ignoré par la configuration de `sshd`, le
// compte peut avoir un shell interdit, le `HOME` peut ne pas être celui qu'on
// croit. Tout ça se voit ici, et nulle part ailleurs.
//
// La clé PRIVÉE n'est pas dans le pod d'enrôlement : c'est l'agent qui la
// détient. On se contente donc de vérifier que la machine ANNONCE désormais
// accepter la clé publique posée, ce que fait une tentative d'authentification
// par clé publique sans signature.
func verifier(ctx context.Context, adresse string, c Cible, empreinte string) error {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.PublicKey))
	if err != nil {
		return fmt.Errorf("clé publique illisible : %w", err)
	}

	epinglee := &empreinteVue{attendue: empreinte}
	client, err := composer(ctx, adresse, ssh.ClientConfig{
		User: c.Login,
		Auth: []ssh.AuthMethod{
			// `PublicKeysCallback` avec un signataire qui ne signe pas :
			// l'échange s'arrête au « cette clé est-elle acceptée ? », qui est
			// exactement la question posée.
			ssh.PublicKeys(sondeur{pub}),
			// Repli : la machine peut exiger une signature. Le mot de passe
			// reste valable et prouve au moins que la session s'ouvre.
			ssh.Password(string(c.Password)),
		},
		HostKeyCallback: epinglee.verifier,
		Timeout:         DelaiConnexion,
	})
	if err != nil {
		if errors.Is(err, ErrEmpreinteChangee) {
			// L'empreinte a changé ENTRE les deux sessions. Très improbable et
			// très grave : le dire tel quel.
			return err
		}
		return fmt.Errorf("vérification de l'enrôlement : %w", err)
	}
	_ = client.Close()
	return nil
}

// sondeur présente une clé publique sans jamais signer.
//
// `ssh.PublicKeys` exige un `Signer`. Celui-ci n'en est un qu'à moitié : il
// annonce la clé, ce qui suffit à savoir si la machine l'accepte, et refuse de
// signer parce qu'il n'a rien pour le faire — la partie privée vit dans un
// agent, ailleurs.
type sondeur struct{ pub ssh.PublicKey }

func (s sondeur) PublicKey() ssh.PublicKey { return s.pub }

func (s sondeur) Sign(_ io.Reader, _ []byte) (*ssh.Signature, error) {
	return nil, errors.New("clé de sondage : aucune signature possible")
}

// estRefusDAuth reconnaît un refus d'authentification.
//
// Sur le TYPE d'erreur et non sur son texte quand c'est possible ; `x/crypto/ssh`
// ne donne malheureusement pas de type dédié, donc le repli est une sous-chaîne.
// Ce que ça coûte est borné : au pire l'erreur est rendue moins précise, jamais
// une conduite différente — les deux chemins échouent.
func estRefusDAuth(err error) bool {
	var refus *ssh.PartialSuccessError
	if errors.As(err, &refus) {
		return true
	}
	return strings.Contains(err.Error(), "unable to authenticate")
}

// resume borne ce qu'on rapporte de la sortie distante.
//
// Elle vient d'une machine qu'on ne contrôle pas et part dans un event d'audit.
func resume(s string) string {
	s = strings.TrimSpace(s)
	const max = 512
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

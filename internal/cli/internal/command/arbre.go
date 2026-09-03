package command

// Rubriques de l'index. Elles ordonnent la page d'accueil, et cet ordre est
// celui d'une prise en main : ouvrir une session, puis lancer un run, puis
// administrer.
const (
	RubriqueSession = "Session"
	RubriqueRuns    = "Lancer un run"
	RubriqueAdmin   = "Administration"
)

// Arbre est la description de toute la surface de la CLI.
//
// UNE seule déclaration, et c'est ce qui a remplacé la constante `texteUsage` de
// cent lignes. Celle-ci était affichée intégralement à la moindre faute de
// frappe, ce qui revenait à ne rien répondre : la réponse à « je me suis trompé
// de drapeau » ne peut pas être le manuel entier.
func Arbre() *Commande {
	return &Commande{
		Nom:    "warden",
		Resume: "warden — exécution auditée de playbooks Ansible",
		Sous: []Commande{
			login(), logout(), whoami(), passwd(),
			submit(),
			repo(), utilisateur(), droit(), cle(), hote(), enroler(),
		},
	}
}

func login() Commande {
	return Commande{
		Nom: "login", Rubrique: RubriqueSession,
		Resume: "ouvre une session sur le serveur",
		Usage:  []string{"login --subject <compte>"},
		Details: "Le mot de passe est demandé au terminal. Il ne passe JAMAIS par argv\n" +
			"ni par l'environnement — les deux sont lisibles par les autres processus\n" +
			"de la machine.\n\n" +
			"Le jeton est rangé dans ~/.config/warden/credentials.json, en 0600,\n" +
			"indexé par adresse de serveur : plusieurs serveurs cohabitent.",
		Flags:    []string{"subject", "ask-password", "password-file", "password-stdin"},
		Exemples: []Exemple{{Ligne: "warden login --subject alice"}},
		Voir:     []string{"warden whoami", "warden logout"},
	}
}

func logout() Commande {
	return Commande{
		Nom: "logout", Rubrique: RubriqueSession,
		Resume: "ferme la session locale",
		Usage:  []string{"logout"},
	}
}

func whoami() Commande {
	return Commande{
		Nom: "whoami", Rubrique: RubriqueSession,
		Resume: "demande au SERVEUR qui il croit que vous êtes",
		Usage:  []string{"whoami"},
		Details: "La question est posée au serveur, et non lue du jeton local : ce qui\n" +
			"compte est ce qu'il accorde, pas ce que la session prétend.",
	}
}

func passwd() Commande {
	return Commande{
		Nom: "passwd", Rubrique: RubriqueSession,
		Resume:  "change votre mot de passe et coupe toutes vos sessions",
		Usage:   []string{"passwd"},
		Details: "Les deux mots de passe sont demandés au terminal. Toutes les sessions sont\ncoupées, y compris celle-ci : c'est le geste qu'on fait quand on croit un\nmot de passe compromis.",
		Flags: []string{
			"ask-current-password", "current-password-file", "current-password-stdin",
			"ask-new-password", "new-password-file", "new-password-stdin",
		},
	}
}

func submit() Commande {
	return Commande{
		Nom: "submit", Rubrique: RubriqueRuns,
		Resume: "lance un playbook depuis le dépôt courant",
		Usage: []string{
			"submit --playbook <fichier> [--repo <nom>] [--env <nom>] [--inventory <chemin>]",
			"submit --playbook <fichier> [--check] [--diff] [--limit <motif>]",
			"submit --playbook <fichier> [-e <nom>=<valeur>]...",
		},
		Details: "Le dépôt est celui où vous vous trouvez. `--repo` est inutile quand le\n" +
			"fichier de projet déclare `repo:` ; le drapeau reste prioritaire.\n\n" +
			"Ce qui s'exécute est résolu par le SERVEUR depuis le commit d'audit, et\n" +
			"non par cette commande : ce qui s'applique et ce qui est audité sont le\n" +
			"même octet.\n\n" +
			"--check, --diff, --limit et -e portent le nom qu'ansible-playbook leur\n" +
			"donne et font la même chose. Ce sont les SEULS acceptés : le serveur\n" +
			"refuse tout autre drapeau, en nommant ceux qu'il prend.\n\n" +
			"-e ne pose que les variables que le fichier de projet DU COMMIT déclare\n" +
			"sous `extra_vars:`. Une variable non déclarée est refusée en nommant\n" +
			"celles qui le sont. C'est le même mécanisme que `forward_env`, avec une\n" +
			"différence qui compte : les valeurs de -e sont AUDITÉES — elles sont\n" +
			"enregistrées sur le run et servies par l'API. N'y mettez pas de secret ;\n" +
			"`forward_env` est le chemin dont les valeurs ne sont jamais journalisées.",
		Flags: []string{"repo", "playbook", "env", "inventory", "run-ref",
			"check", "diff", "limit", "e", "extra-vars",
			"K", "ask-become-pass", "become-pass-file", "become-pass-stdin"},
		Exemples: []Exemple{
			{Ligne: "warden submit --playbook play.yml --env prod --inventory hosts"},
			{Pourquoi: "voir ce qui changerait, sans rien changer",
				Ligne: "warden submit --playbook play.yml --env prod --check --diff"},
			{Pourquoi: "n'agir que sur une machine, ou un groupe de l'inventaire",
				Ligne: "warden submit --playbook play.yml --env prod --limit web-01"},
			{Pourquoi: "poser des variables, déclarées par `extra_vars:` dans le projet",
				Ligne: "warden submit --playbook play.yml --env prod -e version=1.2.3 -e couleur=bleu"},
			{Pourquoi: "avec le mot de passe d'élévation, comme ansible-playbook",
				Ligne: "warden submit --playbook play.yml --env prod -K"},
			{Pourquoi: "reprendre une soumission interrompue produit le même commit d'audit",
				Ligne: "warden submit --playbook play.yml --run-ref 01JZQ..."},
		},
		Voir: []string{"warden repo declare"},
	}
}

func repo() Commande {
	return Commande{
		Nom: "repo", Rubrique: RubriqueAdmin,
		Resume: "déclare et liste les dépôts connus du serveur",
		Details: "Un dépôt doit être connu du serveur avant qu'on puisse y lancer un run.\n" +
			"Réservé aux administrateurs.",
		Sous: []Commande{
			{
				Nom:    "declare",
				Resume: "déclare LE DÉPÔT COURANT",
				Usage: []string{
					"repo declare [--name <nom>] [--remote-url <url>] [--mainline-ref <ref>]",
					"repo declare [--remote <origin>] [--yes]",
				},
				Details: "Le nom, le remote et la branche de référence sont déduits de git, puis\n" +
					"AFFICHÉS et confirmés avant l'écriture.\n\n" +
					"La question n'est pas une politesse : quand le dépôt n'a jamais été\n" +
					"cloné, git ne connaît pas la branche par défaut du remote, et c'est la\n" +
					"branche COURANTE qui sert de repli. Déclarer depuis une branche de\n" +
					"feature épinglerait le mauvais point de comparaison pour tous les runs.\n\n" +
					"Le serveur ne sait cloner qu'en http(s) : si votre origin est une URL\n" +
					"SSH, donnez la forme http du même dépôt avec --remote-url.",
				Flags: []string{"name", "remote-url", "mainline-ref", "remote", "yes"},
				Exemples: []Exemple{
					{Pourquoi: "tout est déduit du dépôt où l'on se trouve", Ligne: "warden repo declare"},
					{Pourquoi: "origin est en SSH", Ligne: "warden repo declare --remote-url https://forge.example.com/equipe/mon-depot.git"},
				},
			},
			{
				Nom:    "list",
				Resume: "affiche les dépôts connus",
				Usage:  []string{"repo list"},
			},
		},
	}
}

func utilisateur() Commande {
	return Commande{
		Nom: "user", Rubrique: RubriqueAdmin,
		Resume:  "administre les comptes",
		Details: "Réservé aux administrateurs.",
		Sous: []Commande{
			{
				Nom:    "add",
				Resume: "crée un compte",
				Usage:  []string{"user add --subject <compte> [--name <nom>] [--admin]"},
				Details: "Le mot de passe est TIRÉ par le serveur et affiché une seule fois : il\n" +
					"n'est stocké nulle part en clair, et personne ne peut le relire.",
				Flags:    []string{"subject", "name", "admin"},
				Exemples: []Exemple{{Ligne: "warden user add --subject alice --name Alice"}},
			},
			{Nom: "list", Resume: "affiche les comptes", Usage: []string{"user list"}},
			{
				Nom: "passwd", Resume: "réinitialise le mot de passe d'un compte",
				Usage: []string{"user passwd --subject <compte>"}, Flags: []string{"subject"},
			},
			{
				Nom: "disable", Resume: "désactive un compte",
				Usage: []string{"user disable --subject <compte>"}, Flags: []string{"subject"},
			},
			{
				Nom: "enable", Resume: "réactive un compte",
				Usage: []string{"user enable --subject <compte>"}, Flags: []string{"subject"},
			},
		},
	}
}

func droit() Commande {
	return Commande{
		Nom: "grant", Rubrique: RubriqueAdmin,
		Resume: "affiche les droits par environnement (lecture seule)",
		Details: "AUCUNE commande ne modifie les droits. Ils viennent d'un document\n" +
			"versionné dans le dépôt de déploiement, que le rôle « controller »\n" +
			"réconcilie :\n\n" +
			"    environments:\n" +
			"      prod:\n" +
			"        - alice\n" +
			"        - bob\n" +
			"      staging: []          # explicitement personne\n\n" +
			"Pour chaque environnement NOMMÉ, la liste fait autorité : un compte retiré\n" +
			"du document est révoqué. Un environnement ABSENT n'est pas touché — les\n" +
			"droits se découpent ainsi en plusieurs fichiers, et un fichier tronqué ne\n" +
			"révoque pas tout.\n\n" +
			"Sans droit, pas de clé d'accès à l'environnement. AVEC le droit, rien ne\n" +
			"borne ce que le playbook peut joindre : l'egress du runner est ouvert.",
		Sous: []Commande{
			{
				Nom:     "list",
				Resume:  "affiche les droits, révoqués compris",
				Usage:   []string{"grant list [--subject <compte>]"},
				Details: "Les révoqués sont affichés : c'est une vue d'audit.",
				Flags:   []string{"subject"},
			},
		},
	}
}

func cle() Commande {
	return Commande{
		Nom: "key", Rubrique: RubriqueAdmin,
		Resume:  "administre les clés d'accès des environnements",
		Details: "Une clé par environnement. La partie privée ne quitte jamais le cluster.",
		Sous: []Commande{
			{
				Nom: "generate", Resume: "frappe la paire de clés d'un environnement",
				Usage: []string{"key generate --env <nom>"}, Flags: []string{"env"},
			},
			{
				Nom: "import", Resume: "importe une clé privée existante",
				Usage: []string{"key import --env <nom> --key-file <chemin> | --key-stdin"},
				Details: "La saisie au terminal est refusée pour ce secret-ci : une clé PEM ne se\n" +
					"colle pas à l'aveugle, l'utilisateur ne verrait rien de ce qu'il tape.",
				Flags: []string{"env", "ask-key", "key-file", "key-stdin"},
			},
			{
				Nom: "show", Resume: "affiche la clé publique à poser dans authorized_keys",
				Usage: []string{"key show --env <nom>"}, Flags: []string{"env"},
				Voir: []string{"warden host declare"},
			},
			{Nom: "list", Resume: "affiche les environnements pourvus d'une clé", Usage: []string{"key list"}},
		},
	}
}

func hote() Commande {
	return Commande{
		Nom: "host", Rubrique: RubriqueAdmin,
		Resume: "administre l'inventaire des machines enrôlées",
		Details: "Cet inventaire dit COMMENT warden joint une machine, et sur quelle\n" +
			"empreinte. Ce n'est pas une source d'inventaire Ansible : ce qui décide\n" +
			"de ce qui s'exécute vient du commit d'audit.",
		Sous: []Commande{
			{
				Nom:    "declare",
				Resume: "enregistre des machines dont la clé est DÉJÀ posée",
				Usage: []string{
					"host declare -f <fichier.yaml> [--env <nom>]",
					"host declare --from-inventory [--env <nom>] [--group <nom>] [--dry-run]",
				},
				Details: "warden n'a rien posé sur ces machines : il enregistre. Leur\n" +
					"authorized_keys doit déjà contenir la clé de l'environnement.\n\n" +
					"Format du fichier de déclaration :\n\n" +
					"    hosts:\n" +
					"      - host: db-prod-02      # le nom TEL QUE l'inventaire Ansible le nomme\n" +
					"        address: 10.20.0.42   # une adresse, ou un nom que LE POD sait résoudre\n" +
					"        login: ansible        # le compte que warden ouvre\n" +
					"        port: 22              # facultatif\n" +
					"        host_key: \"ssh-ed25519 AAAA...\"   # facultatif, mais c'est lui\n" +
					"                                          # qui protège du premier\n" +
					"                                          # contact aveugle\n\n" +
					"--from-inventory lit l'inventaire ANSIBLE du dépôt courant à la place.\n" +
					"Voir « warden help enroll » pour ce qui en est lu, et ce qui ne l'est pas.",
				Flags: []string{"f", "from-inventory", "env", "group", "dry-run"},
				Exemples: []Exemple{
					{Pourquoi: "voir ce qui manque au serveur, sans rien écrire",
						Ligne: "warden host declare --from-inventory --env prod --dry-run"},
					{Ligne: "warden host declare -f machines.yaml --env prod"},
				},
				Voir: []string{"warden key show", "warden enroll"},
			},
			{
				Nom: "list", Resume: "affiche les machines enrôlées",
				Usage: []string{"host list [--env <nom>]"}, Flags: []string{"env"},
				Details: "La colonne EMPREINTE dit « AUCUNE » pour une machine non épinglée : son\n" +
					"premier contact fera confiance à ce qui répond.",
			},
			{
				Nom: "forget", Resume: "retire une machine de l'inventaire",
				Usage: []string{"host forget --env <nom> --host <machine>"},
				Details: "Retire la LIGNE, pas la clé. La clé publique reste dans l'authorized_keys\n" +
					"de la machine, et warden ne sait pas l'en retirer : ce n'est PAS une\n" +
					"révocation.",
				Flags: []string{"env", "host"},
			},
		},
	}
}

func enroler() Commande {
	return Commande{
		Nom: "enroll", Rubrique: RubriqueAdmin,
		Resume: "pose la clé de l'environnement sur une machine, ou sur toute la flotte",
		Usage: []string{
			"enroll --env <nom> --host <machine> --address <ip|nom> --login <compte>",
			"enroll --all [--env <nom>] [--group <nom>] [--dry-run]",
		},
		Details: "warden se connecte par mot de passe, capture l'empreinte, ajoute la clé\n" +
			"aux authorized_keys, puis SE RECONNECTE AVEC LA CLÉ — c'est cette dernière\n" +
			"étape qui distingue « la clé est posée » de « la machine est joignable ».\n\n" +
			"L'empreinte constatée vous est ensuite soumise. C'est le seul moment où un\n" +
			"premier contact aveugle peut encore être refusé, et la réponse par défaut\n" +
			"est NON.\n\n" +
			"--all lit l'inventaire ANSIBLE du dépôt courant, le compare au serveur, et\n" +
			"n'enrôle QUE les machines qui manquent — une par une, avec un seul mot de\n" +
			"passe. Sont lus, par machine ou par groupe :\n\n" +
			"    ansible_host   l'adresse. ABSENTE, c'est le NOM de la machine qui sert\n" +
			"                   d'adresse, et il sera résolu DEPUIS LE CLUSTER — pas\n" +
			"                   depuis votre poste, et sans lire votre ssh_config\n" +
			"    ansible_user   le compte que warden ouvre\n" +
			"    ansible_port   facultatif\n\n" +
			"group_vars/ et host_vars/ ne sont PAS lus : reproduire à peu près la\n" +
			"précédence d'Ansible enrôlerait des machines sous le mauvais compte sans\n" +
			"que rien ne le dise. Une valeur Jinja est refusée pour la même raison.\n\n" +
			"Les machines déjà enrôlées dont l'inventaire dit autre chose sont\n" +
			"signalées, et pas touchées.",
		Flags: []string{
			"env", "host", "address", "port", "login", "run-ref",
			"ask-password", "password-file", "password-stdin",
			"all", "group", "dry-run", "yes",
			"password-per-host", "accept-new-hostkeys", "stop-on-error",
		},
		Exemples: []Exemple{
			{Pourquoi: "une machine", Ligne: "warden enroll --env prod --host db-01 --address 10.20.0.42 --login ansible"},
			{Pourquoi: "toute la flotte, en regardant d'abord", Ligne: "warden enroll --all --env prod --dry-run"},
			{Pourquoi: "un groupe de l'inventaire, comptes hétérogènes", Ligne: "warden enroll --all --env prod --group distant --password-per-host"},
		},
		Voir: []string{"warden key show", "warden host list"},
	}
}

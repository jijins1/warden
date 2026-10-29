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
			completion(), upgrade(),
			submit(), run(),
			logs(),
			repo(), utilisateur(), droit(), cle(), vault(), hote(), enroler(),
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

func completion() Commande {
	return Commande{
		Nom: "completion", Rubrique: RubriqueSession,
		Resume: "affiche le script d'auto-complétion d'un shell",
		Usage:  []string{"completion bash", "completion zsh"},
		Details: "Le script rappelle `warden` à chaque Tab — il ne connaît aucune commande\n" +
			"par cœur, donc il ne peut jamais proposer un verbe ou un drapeau que\n" +
			"cette CLI n'a pas.\n\n" +
			"Un geste de mise en place, comme `warden login` : à faire une fois, dans\n" +
			"le fichier de démarrage du shell. C'est la ligne `source` qu'on y pose,\n" +
			"pas le script : régénéré à chaque shell, il suit toujours le binaire.",
		Exemples: []Exemple{
			{Pourquoi: "bash, une fois pour toutes", Ligne: "echo 'source <(warden completion bash)' >> ~/.bashrc"},
			{Pourquoi: "zsh, une fois pour toutes", Ligne: "echo 'source <(warden completion zsh)' >> ~/.zshrc"},
		},
	}
}

func upgrade() Commande {
	return Commande{
		Nom: "upgrade", Rubrique: RubriqueSession,
		Resume: "met la CLI à la version du serveur",
		Usage:  []string{"upgrade"},
		Details: "Télécharge la release de la version qu'annonce le serveur, vérifie sa\n" +
			"signature minisign par la clé inscrite dans cette CLI, puis remplace le\n" +
			"binaire. Rien n'est installé qui ne soit signé pour CETTE version.\n\n" +
			"« warden submit » et « warden run » font la même vérification et\n" +
			"refusent de soumettre tant que la CLI n'est pas à la version du serveur.\n\n" +
			"WARDEN_UPDATE_URL remplace l'hôte des releases par un miroir, qui\n" +
			"reproduit l'arborescence v<version>/… ; la signature y est vérifiée de\n" +
			"la même façon.",
		Exemples: []Exemple{{Ligne: "warden upgrade"}},
	}
}

func submit() Commande {
	return Commande{
		Nom: "submit", Rubrique: RubriqueRuns,
		Resume: "lance un playbook depuis le dépôt courant",
		Usage: []string{
			"submit --playbook <fichier> [--repo <nom>] [--env <nom>] [--inventory <chemin>]",
			"submit --playbook <fichier> [--check] [--diff] [--limit <motif>] [-v|-vv|-vvv|-vvvv]",
			"submit --playbook <fichier> [--tags <t1,t2>] [--skip-tags <t1,t2>]",
			"submit --playbook <fichier> [-b] [--become-user <compte>] [-K]",
			"submit --playbook <fichier> -u <compte> -k   # machines sans enrôlement (annuaire LDAP)",
			"submit --playbook <fichier> [-e <nom>=<valeur>]...",
		},
		Details: "Le dépôt est celui où vous vous trouvez. `--repo` est inutile quand le\n" +
			"fichier de projet déclare `repo:` ; le drapeau reste prioritaire.\n\n" +
			"Ce qui s'exécute est résolu par le SERVEUR depuis le commit d'audit, et\n" +
			"non par cette commande : ce qui s'applique et ce qui est audité sont le\n" +
			"même octet.\n\n" +
			"La commande SUIT le run et affiche son déroulement, comme\n" +
			"ansible-playbook. Elle rend un code non nul si le run échoue, ce qui\n" +
			"la rend utilisable dans un pipeline. --no-follow rend la main dès le\n" +
			"run déclaré ; « warden logs » rattrape le déroulement.\n\n" +
			"Ctrl-C DÉTACHE la sortie, il n'annule pas le run : celui-ci continue\n" +
			"côté serveur. L'annulation demande un endpoint que le serveur n'expose\n" +
			"pas encore.\n\n" +
			"--check, --diff, --limit, --tags, --skip-tags, -v, -e et -b portent le\n" +
			"nom qu'ansible-playbook leur donne et font la même chose. Ce sont les\n" +
			"SEULS acceptés : le serveur refuse tout autre drapeau, en nommant ceux\n" +
			"qu'il prend.\n\n" +
			"--tags et --skip-tags prennent des noms de tag séparés par des virgules,\n" +
			"sans espace. Ils sont enregistrés sur le run et la page du run les met\n" +
			"en avant : ce qui ne s'est pas exécuté se lit autant que ce qui l'a été.\n\n" +
			"-v à -vvvv règlent la verbosité, et ils s'écrivent comme chez\n" +
			"ansible-playbook : les `v` se répètent dans UN drapeau, `-v -v` ne fait\n" +
			"pas -vv. Donnés plusieurs fois, le niveau le plus élevé l'emporte. Ce\n" +
			"qu'ils changent est ce que le run ENREGISTRE, pas ce qu'il exécute :\n" +
			"la sortie détaillée d'Ansible arrive dans le flux d'events, donc dans\n" +
			"l'audit. À partir de -vvv, Ansible imprime les arguments de chaque\n" +
			"tâche et la commande de connexion ; les valeurs injectées par warden\n" +
			"restent masquées, et `no_log` est respecté, mais ce qu'un playbook met\n" +
			"en clair dans ses tâches devient lisible.\n\n" +
			"-b demande l'élévation, -K en fournit le mot de passe : les deux vont\n" +
			"ensemble quand la machine en demande un. -b s'applique à TOUTES les\n" +
			"tâches, y compris celles que le playbook avait laissées non élevées ;\n" +
			"c'est enregistré sur le run, donc lisible en audit.\n\n" +
			"-e ne pose que les variables que le fichier de projet DU COMMIT déclare\n" +
			"sous `extra_vars:`. Une variable non déclarée est refusée en nommant\n" +
			"celles qui le sont. C'est le même mécanisme que `forward_env`, avec une\n" +
			"différence qui compte : les valeurs de -e sont AUDITÉES — elles sont\n" +
			"enregistrées sur le run et servies par l'API. N'y mettez pas de secret ;\n" +
			"`forward_env` est le chemin dont les valeurs ne sont jamais journalisées.",
		Flags: []string{"repo", "playbook", "env", "inventory", "run-ref",
			"check", "diff", "limit", "tags", "skip-tags", "v", "vv", "vvv", "vvvv", "e", "extra-vars",
			"b", "become", "become-user", "no-follow",
			"K", "ask-become-pass", "become-pass-file", "become-pass-stdin",
			"k", "ask-pass", "pass-file", "pass-stdin", "u", "user"},
		Exemples: []Exemple{
			{Ligne: "warden submit --playbook play.yml --env prod --inventory hosts"},
			{Pourquoi: "voir ce qui changerait, sans rien changer",
				Ligne: "warden submit --playbook play.yml --env prod --check --diff"},
			{Pourquoi: "n'agir que sur une machine, ou un groupe de l'inventaire",
				Ligne: "warden submit --playbook play.yml --env prod --limit web-01"},
			{Pourquoi: "ne jouer que les tâches de déploiement, sans les plus lentes",
				Ligne: "warden submit --playbook play.yml --env prod --tags deploy,config --skip-tags slow"},
			{Pourquoi: "comprendre ce qu'une tâche a réellement fait",
				Ligne: "warden submit --playbook play.yml --env prod -vv"},
			{Pourquoi: "poser des variables, déclarées par `extra_vars:` dans le projet",
				Ligne: "warden submit --playbook play.yml --env prod -e version=1.2.3 -e couleur=bleu"},
			{Pourquoi: "élever, et fournir le mot de passe que sudo demandera",
				Ligne: "warden submit --playbook play.yml --env prod -b -K"},
			{Pourquoi: "élever vers un autre compte que root",
				Ligne: "warden submit --playbook play.yml --env prod -b --become-user postgres"},
			{Pourquoi: "reprendre une soumission interrompue produit le même commit d'audit",
				Ligne: "warden submit --playbook play.yml --run-ref 01JZQ..."},
		},
		Voir: []string{"warden logs", "warden repo declare", "warden run"},
	}
}

func run() Commande {
	return Commande{
		Nom: "run", Rubrique: RubriqueRuns,
		Resume: "exécute un module Ansible contre un motif de machines, sans playbook",
		Usage: []string{
			"run <motif> -m <module> [-a <args>] [--repo <nom>] [--env <nom>] [--inventory <chemin>]",
			"run <motif> -m <module> [--check] [--diff] [-v|-vv|-vvv|-vvvv]",
			"run <motif> -m <module> [-b] [--become-user <compte>] [-K]",
			"run <motif> -m <module> [-e <nom>=<valeur>]...",
		},
		Details: "L'équivalent de la commande `ansible` — par opposition à\n" +
			"`ansible-playbook`, que « warden submit » exécute. Utile pour débugger :\n" +
			"un statut de service, un `uptime`, le contenu d'un fichier, sans écrire\n" +
			"de playbook jetable.\n\n" +
			"Le motif est POSITIONNEL, comme chez `ansible` : c'est le premier\n" +
			"argument, pas un drapeau. -m (ou --module) est requis ; -a (ou --args)\n" +
			"porte les arguments du module, vides pour un module qui n'en prend pas\n" +
			"(`ping`, `setup`).\n\n" +
			"Le reste — dépôt, environnement, inventaire, élévation, extra-vars,\n" +
			"verbosité, suivi du run — se comporte exactement comme « warden submit » : un\n" +
			"ad-hoc capture et pousse un commit d'audit pour résoudre son motif dans\n" +
			"le même inventaire, et reste donc une action auditée au même titre\n" +
			"qu'un playbook.\n\n" +
			"Aucune liste de modules autorisés : un compte qui a accès à un\n" +
			"environnement exécute déjà du code arbitraire via un playbook\n" +
			"(`shell`, `command`) ; en refuser certains ici n'ajouterait rien de réel.",
		Flags: []string{"repo", "module", "m", "args", "a", "env", "inventory", "run-ref",
			"check", "diff", "v", "vv", "vvv", "vvvv", "e", "extra-vars",
			"b", "become", "become-user", "no-follow",
			"K", "ask-become-pass", "become-pass-file", "become-pass-stdin",
			"k", "ask-pass", "pass-file", "pass-stdin", "u", "user"},
		Exemples: []Exemple{
			{Ligne: "warden run all -m ping"},
			{Pourquoi: "un diagnostic simple sur un groupe",
				Ligne: "warden run web -m shell -a \"systemctl status nginx\""},
			{Pourquoi: "élever, et fournir le mot de passe que sudo demandera",
				Ligne: "warden run web -m shell -a \"tail /var/log/app.log\" -b -K"},
		},
		Voir: []string{"warden submit", "warden logs"},
	}
}

func logs() Commande {
	return Commande{
		Nom: "logs", Rubrique: RubriqueRuns,
		Resume: "rattache la sortie au déroulement d'un run",
		Usage:  []string{"logs <identifiant de run>"},
		Details: "Le même affichage que « warden submit », sur un run déjà lancé.\n\n" +
			"Utile après --no-follow, après un Ctrl-C, ou depuis une autre machine :\n" +
			"le flux est rejouable depuis le début, il ne commence pas à l'instant\n" +
			"où l'on s'attache.\n\n" +
			"L'adresse du serveur vient du fichier de projet du dépôt courant, comme\n" +
			"pour submit — on suit le plus souvent un run qu'on vient d'y soumettre.",
		Exemples: []Exemple{
			{Ligne: "warden logs 01JZQ8XKPT9V2H3M4N5P6Q7R8S"},
		},
		Voir: []string{"warden submit"},
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
				Flags:   []string{"name", "remote-url", "mainline-ref", "remote", "yes"},
				Valeurs: map[string]string{"name": "mon-depot"},
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
				Valeurs:  map[string]string{"name": "Alice"},
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

func vault() Commande {
	return Commande{
		Nom: "vault", Rubrique: RubriqueAdmin,
		Resume: "administre la clé Ansible Vault d'un environnement",
		Details: "Une clé par environnement, sur le même découpage que « warden key ».\n" +
			"La valeur en clair ne quitte JAMAIS le serveur : ce paquet ne la\n" +
			"rend à personne, y compris à un administrateur. Le seul chemin\n" +
			"d'interaction humaine est « vault encrypt » — on donne une valeur en\n" +
			"clair, on reçoit un texte chiffré.\n\n" +
			"Remplacer la clé d'un environnement CASSE le déchiffrement des\n" +
			"secrets déjà chiffrés avec l'ancienne, tant qu'ils n'ont pas été\n" +
			"ré-encodés avec « vault encrypt » sous la nouvelle : warden ne\n" +
			"modifie jamais le contenu d'un dépôt.",
		Sous: []Commande{
			{
				Nom: "generate", Resume: "tire le mot de passe de vault d'un environnement",
				Usage: []string{"vault generate --env <nom>"}, Flags: []string{"env"},
			},
			{
				Nom: "import", Resume: "importe un mot de passe existant",
				Usage:   []string{"vault import --env <nom> [--ask-vault | --vault-file <chemin> | --vault-stdin]"},
				Details: "Sans drapeau, le mot de passe est demandé au terminal.",
				Flags:   []string{"env", "ask-vault", "vault-file", "vault-stdin"},
			},
			{
				Nom: "show", Resume: "affiche l'empreinte de la clé de vault",
				Usage: []string{"vault show --env <nom>"}, Flags: []string{"env"},
			},
			{
				Nom: "encrypt", Resume: "chiffre une valeur, rend un bloc !vault à coller dans un playbook",
				Usage: []string{"vault encrypt --env <nom> --var <nom_variable> " +
					"[--ask-value | --value-file <chemin> | --value-stdin]"},
				Details: "Sans drapeau, la valeur est demandée au terminal — le chemin normal\n" +
					"pour un secret court qu'on ne veut voir nulle part dans un\n" +
					"historique de shell.",
				Flags: []string{"env", "var", "ask-value", "value-file", "value-stdin"},
				Exemples: []Exemple{
					{Pourquoi: "chiffrer un mot de passe de base de données pour l'environnement prod",
						Ligne: "warden vault encrypt --env prod --var db_password"},
				},
			},
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

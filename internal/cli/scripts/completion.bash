# Complétion bash pour warden.
#
# Générée par « warden completion bash ». Chaque Tab relance
# « warden __complete » avec les mots déjà tapés : c'est le même arbre de
# commandes que « warden help », donc jamais en désaccord avec lui.
#
# Le backend rend « mot<TAB>description ». bash ne sait pas afficher une
# description : quand plusieurs candidats restent, chacun est rendu comme
# « mot  -- description » et complété jusqu'à la largeur du terminal, ce qui
# force une ligne par candidat. Quand il n'en reste qu'un, seul le mot est
# inséré.
_warden_complete() {
    local IFS=$'\n'
    local -a lignes mots
    local ligne mot largeur=0 colonnes=${COLUMNS:-80}
    lignes=($(warden __complete "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null))
    COMPREPLY=()

    if (( ${#lignes[@]} == 1 )); then
        COMPREPLY=("${lignes[0]%%$'\t'*}")
        return 0
    fi

    for ligne in "${lignes[@]}"; do
        mot=${ligne%%$'\t'*}
        (( ${#mot} > largeur )) && largeur=${#mot}
    done
    for ligne in "${lignes[@]}"; do
        mot=${ligne%%$'\t'*}
        if [[ $ligne == *$'\t'* ]]; then
            ligne=$(printf '%-*s  -- %s' "$largeur" "$mot" "${ligne#*$'\t'}")
        else
            ligne=$mot
        fi
        # Coupé en CARACTÈRES par la sous-chaîne, et non par printf, qui
        # compte en octets et trancherait un accent en deux.
        ligne=${ligne:0:colonnes-1}
        ligne=$(printf '%-*s' "$((colonnes - 1))" "$ligne")
        COMPREPLY+=("$ligne")
    done
    return 0
}
complete -o default -F _warden_complete warden

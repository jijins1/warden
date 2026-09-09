# Complétion bash pour warden.
#
# Générée par « warden completion bash ». Chaque Tab relance
# « warden __complete » avec les mots déjà tapés : c'est le même arbre de
# commandes que « warden help », donc jamais en désaccord avec lui.
_warden_complete() {
    local IFS=$'\n'
    COMPREPLY=($(warden __complete "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null))
    return 0
}
complete -o default -F _warden_complete warden

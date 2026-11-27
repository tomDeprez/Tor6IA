# Changelog

Each `## vX.Y.Z` section becomes the notes of that release, and the text of the update window in the program.

## v0.2.0

**Français**
- Un modèle peut être réparti sur plusieurs machines de confiance : « Répartir un modèle sur plusieurs machines… ». Ta machine le propose au réseau et reçoit le paiement.
- L'interface affiche le découpage : un segment par bloc, et quelle machine porte quelles couches.
- Si une machine du groupe s'en va, l'interface le dit et ta machine continue seule si elle en a la mémoire.

**English**
- A model can be spread over several machines you trust: "Spread a model over several machines…". Your machine offers it to the network and is paid for it.
- The interface shows the split: one segment per block, and which machine holds which layers.
- If a machine of the group leaves, the interface says so and your machine goes on alone when it has the memory.

## v0.1.4

**Français**
- Llama 3.2 3B revient comme modèle léger, pour les machines de 6 à 8 Go de mémoire où Gemma 4 E2B ne tient pas.
- La règle mémoire suit la mesure réelle : une machine ne propose plus un modèle qu'Ollama refuserait de charger.
- Chaque machine démarre avec le plus gros modèle qu'elle fait tourner vite.

**English**
- Llama 3.2 3B is back as the light model, for machines with 6 to 8 GB of memory where Gemma 4 E2B does not fit.
- The memory rule follows what was measured: a machine no longer offers a model Ollama would refuse to load.
- Each machine starts with the heaviest model it runs fast.

## v0.1.3

**Français**
- Une machine partage un seul modèle à la fois : « Choisir » remplace le modèle en cours, et propose de supprimer l'ancien du disque.
- Pendant qu'une demande attend, l'interface dit pourquoi : combien de machines sont sur ce modèle, et si elles sont toutes occupées.
- La liste des modèles montre les machines occupées.

**English**
- A machine shares one model at a time: "Choose" replaces the current model, and offers to delete the old one from the disk.
- While a request waits, the interface says why: how many machines are on that model, and whether they are all busy.
- The model list shows the busy machines.

## v0.1.2

**Français**
- Nouveaux modèles : Gemma 4 en quatre tailles (E2B, 12B, 26B, 31B) remplace Llama 3.2 et Gemma 3. Les anciens ne sont plus proposés.
- Une machine sans assez de mémoire pour un modèle ne le charge jamais.
- Nouvelle interface : bloc d'état avec une ligne de pouls qui s'anime pendant un calcul, jauge de solde avec le zéro et le découvert.
- Sécurité : la clé de chiffrement est signée par la machine payée. Le coordinateur ne peut plus intercepter une demande destinée à la machine de quelqu'un d'autre.
- Le coordinateur limite le débit par adresse : une inondation de requêtes ne gêne plus les autres.
- « Supprimer les fichiers » est coché par défaut quand tu arrêtes de partager un modèle.

**English**
- New models: Gemma 4 in four sizes (E2B, 12B, 26B, 31B) replaces Llama 3.2 and Gemma 3. The old ones are no longer offered.
- A machine without enough memory for a model never loads it.
- New interface: a state block with a pulse line that moves during a job, and a balance gauge showing zero and the overdraft.
- Security: the encryption key is signed by the machine that is paid. The coordinator can no longer intercept a request meant for someone else's machine.
- The coordinator limits the request rate per address: a flood no longer slows everyone else.
- "Delete the files" is ticked by default when you stop sharing a model.

## v0.1.1

**Français**
- Partage plusieurs modèles depuis la même machine : bouton « Partager » sur chaque modèle, avec confirmation de ce que ça implique.
- Arrête de partager un modèle et supprime ses fichiers du disque en un clic.
- Annule un téléchargement en cours.
- Désinstallation complète depuis l'interface.
- Quand une machine se met en pause ou s'en va, les autres le voient tout de suite.
- Si une machine lâche avant de répondre, la demande repart sur une autre, sans rien coûter.
- Le calcul n'utilise que la moitié des processeurs : ta machine reste utilisable.
- Le programme propose ses mises à jour tout seul.

**English**
- Share several models from the same machine: a "Share" button on each model, which confirms what it implies.
- Stop sharing a model and delete its files from the disk in one click.
- Cancel a download in progress.
- Full uninstall from the interface.
- When a machine pauses or leaves, the others see it at once.
- If a machine goes away before answering, the request moves to another one, at no cost.
- Computing uses half of the processors: your machine stays usable.
- The program offers its own updates.

## v0.1.0

**Français**
- Première version : réseau pair à pair, registre public signé, chiffrement de bout en bout, API compatible OpenAI.

**English**
- First release: peer-to-peer network, signed public ledger, end-to-end encryption, OpenAI-compatible API.

# Changelog

Each `## vX.Y.Z` section becomes the notes of that release, and the text of the update window in the program.

## v0.5.0

**Français**
- Toute la chaîne est payée. Une réponse d'un modèle en blocs paie la machine qui tient la conversation (2 parts sur 10) et les porteurs (8 parts, selon leurs blocs). Le demandeur signe cette liste, le registre public la contient, `tor6ia audit` la vérifie.
- Une machine qui calcule faux est trouvée et exclue pour un jour : le coordinateur remplace un porteur à la fois et refait le test. Un porteur qui renvoie du bruit est dehors en 4 tests, environ une minute.
- Avant une question à un modèle en blocs, l'interface dit ce que voient les machines de la chaîne.
- Charger un modèle en blocs n'envoie plus un cinquième du modèle sur le réseau : 2 Mo au lieu de 411 Mo pour Llama 3.2 3B à travers le relais.
- Corrigé : dans la version 0.4.0, le bouton « Tenir ces conversations » ne répondait pas, et valider une autre fenêtre pouvait envoyer cette demande à sa place.
- Le registre contient un nouveau genre de paiement : les versions d'avant ne savent pas le vérifier et afficheront une alerte. Mets à jour.

**English**
- Everyone in a chain is paid. An answer from a model in blocks pays the machine that holds the conversation (2 parts in 10) and the holders (8 parts, by their blocks). The requester signs that list, the public ledger holds it, `tor6ia audit` checks it.
- A machine that computes wrong is found and excluded for a day: the coordinator replaces one holder at a time and tests again. A holder that answers noise is out in 4 tests, about a minute.
- Before a question to a model in blocks, the interface says what the machines of the chain see.
- Loading a model in blocks no longer sends a fifth of the model over the network: 2 MB instead of 411 MB for Llama 3.2 3B through the relay.
- Fixed: in version 0.4.0 the button "Hold these conversations" did not answer, and confirming another window could send that request in its place.
- The ledger holds a new kind of payment: earlier versions cannot verify it and will show an alert. Update.

## v0.4.0

**Français**
- Le réseau forme les groupes tout seul. Des machines portent des blocs, une machine tient les conversations, et le coordinateur les assemble en chaîne dès que chaque bloc a ses porteurs. Plus de ligne à échanger.
- « Tenir les conversations d'un modèle en blocs… » : ta machine reçoit les questions d'un modèle dont les couches sont sur d'autres machines. Il lui faut le fichier du modèle, peu de mémoire.
- La liste des modèles affiche les blocs portés : « En blocs : 4 blocs sur 6 portés », puis « chaîne formée ».
- La fenêtre « Porter des blocs » propose les blocs dont le réseau manque le plus.
- Un relais joint les machines qui ne peuvent pas se joindre directement : porter des blocs derrière une box ne demande aucun port ouvert. Il ne voit jamais ce qui passe.
- Quand une machine part au milieu d'une réponse, une autre reprend là où le texte s'était arrêté : la réponse se termine au lieu d'échouer. Vrai aussi pour les modèles entiers.
- Deux porteurs sur la même machine ne se disputent plus le port.

**English**
- The network forms the groups by itself. Machines hold blocks, one machine holds the conversations, and the coordinator puts them in a chain as soon as every block has its holders. No more line to exchange.
- "Hold the conversations of a model in blocks…": your machine receives the questions of a model whose layers are on other machines. It needs the model's file, little memory.
- The list of models shows the blocks held: "In blocks: 4 blocks of 6 held", then "chain formed".
- The "Hold blocks" window proposes the blocks the network lacks most.
- A relay joins machines that cannot reach each other directly: holding blocks behind a home router needs no open port. It never sees what passes.
- When a machine leaves in the middle of an answer, another one goes on from where the text stopped: the answer is finished instead of failing. True for whole models too.
- Two holders on the same machine no longer fight for the port.

## v0.3.0

**Français**
- « Porter des blocs d'un modèle… » : ta machine porte quelques blocs d'un gros modèle pour un groupe. Elle ne télécharge que ces blocs, pas le modèle entier.
- L'interface lit le plan du modèle sans le télécharger : combien de blocs il a, combien ta machine peut en porter, ce qu'il faut télécharger et la mémoire utilisée.
- Le panneau « Blocs portés par ta machine » affiche les blocs téléchargés, chargés et en service, et la ligne à donner au groupe.
- Un groupe se forme avec les blocs de chaque machine (`adresse=2-3#secret`). Un groupe incomplet est refusé, avec les blocs qui manquent.
- Le serveur de blocs tourne enfermé : il ne peut ni lire tes fichiers, ni se connecter ailleurs, ni lancer un programme, ni dépasser la mémoire de ses blocs.
- Ce qui passe entre les machines d'un groupe est chiffré. Une machine n'obéit qu'à qui a sa ligne, qui contient un secret.

**English**
- "Hold blocks of a model…": your machine holds a few blocks of a large model for a group. It downloads those blocks only, not the whole model.
- The interface reads the plan of the model without downloading it: how many blocks it has, how many your machine can hold, what to download and the memory used.
- The panel "Blocks your machine holds" shows the blocks downloaded, loaded and in service, and the line to give to the group.
- A group is formed with the blocks of each machine (`address=2-3#secret`). An incomplete group is refused, with the blocks that are missing.
- The block server runs locked in: it cannot read your files, connect anywhere, start a program or use more memory than its blocks need.
- What passes between the machines of a group is encrypted. A machine obeys only who has its line, which holds a secret.

## v0.2.1

**Français**
- L'interface sépare « Ta machine partage » et « Utiliser un modèle » : partager un modèle et discuter avec un autre sont deux choix distincts.
- Le modèle partagé indique ses blocs : en entier sur ta machine, ou réparti en plusieurs blocs.
- Tu peux demander un modèle que personne ne porte encore : la demande attend, l'interface dit qu'il manque une machine, et elle part dès qu'une machine le propose.
- Chaque modèle affiche les demandes en attente : on voit quels modèles manquent de machines.

**English**
- The interface separates "Your machine shares" from "Use a model": sharing a model and chatting with another are two distinct choices.
- The shared model shows its blocks: whole on your machine, or split in several blocks.
- You can ask for a model nobody holds yet: the request waits, the interface says a machine is missing, and it leaves as soon as a machine offers it.
- Each model shows the requests that wait: everyone sees which models lack machines.

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

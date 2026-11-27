# Essai à plusieurs : porter un gros modèle ensemble

Ce guide sert à jouer, avec de vraies personnes sur de vraies machines, la scène pour laquelle Tor6IA a été fait : des machines qui ne se connaissent pas portent ensemble un modèle qu'aucune ne tient seule.

Tout ce qui est décrit ici a été testé sur un seul PC (Windows et Linux) avec le coordinateur et le relais publics. Ce qui n'a pas pu l'être est marqué **jamais testé**.

## Ce qu'il faut

- Au moins 4 personnes, chacune avec une machine. 5 ou 6, c'est mieux.
- Sur chaque machine : Tor6IA 0.5.0 ou plus récent, installé depuis la [dernière version](https://github.com/tomDeprez/Tor6IA/releases/latest).
- Pour une des machines : la place sur le disque pour le fichier entier du modèle choisi.

| Modèle | Blocs | Fichier entier | Mémoire d'un porteur, par bloc |
|---|---|---|---|
| Llama 3.2 3B | 2 | 2 Go | 1,3 Go |
| Gemma 4 12B | 6 | 8 Go | 1,4 Go |
| Gemma 4 31B | 16 | 20 Go | 1,5 Go |

Commence par Llama 3.2 3B pour vérifier que tout le monde se voit, puis passe à un gros modèle.

## Les rôles

- **Porteurs** : ils cliquent sur « Porter des blocs d'un modèle… », choisissent le modèle, gardent les blocs proposés et valident. La machine télécharge ces blocs seulement.
- **Une tête** : elle clique sur « Tenir les conversations d'un modèle en blocs… » et choisit le même modèle. Sa machine télécharge le fichier entier.
- **Un demandeur** : n'importe quelle machine du réseau qui partage un modèle depuis quelques minutes (elle a alors droit à un découvert de 200 crédits).

Le réseau public demande **deux porteurs pour chaque bloc** avant de former une chaîne. Avec Llama 3.2 3B : 4 porteurs d'un bloc, ou 2 porteurs de 2 blocs.

## Le déroulé

1. Les porteurs portent leurs blocs. Dans la liste des modèles, chacun voit « En blocs : N blocs sur M portés » monter.
2. La tête se propose. Quand chaque bloc a ses deux porteurs, la liste affiche « chaîne formée » chez tout le monde. Sur la tête, le panneau « Découpage du modèle » montre qui porte quoi, et « par le relais » pour les machines jointes à travers lui.
3. Le demandeur choisit le modèle. Un avertissement dit ce que voient les machines de la chaîne. Il pose une question.
4. Pendant une longue réponse (« Compte de 1 à 200 »), un porteur de la chaîne ferme Tor6IA. La réponse doit s'arrêter un moment, puis reprendre et se terminer.
5. Chacun regarde ses crédits : la tête et les porteurs de la chaîne ont gagné, le demandeur a payé.

## Ce qu'il faut noter

Pour chaque machine : système, mémoire, connexion (fibre, ADSL, 4G), pays.

| Mesure | Où la lire |
|---|---|
| Temps entre le dernier porteur prêt et « chaîne formée » | à la montre |
| Machines jointes « par le relais » ou en direct | panneau « Découpage du modèle » de la tête |
| Tokens par seconde | longueur de la réponse et temps, ou `timings` de l'API |
| Temps perdu quand un porteur part | à la montre |
| Crédits de chacun après 5 questions | compteur de crédits, ou `tor6ia audit` |
| Ce qui a planté, et le message affiché | capture d'écran |

## Ce qui n'a jamais été testé

- **Plus d'un PC.** Tous les essais ont tourné sur une machine, avec ses nœuds Windows et Linux. Deux machines derrière deux box différentes passent par le relais : ce chemin a été testé de bout en bout, mais avec les deux bouts sur la même connexion.
- **Gemma 4 31B en blocs.** Il ne tient pas sur une machine de 32 Go avec tous ses porteurs. Gemma 4 12B a été testé en 3 porteurs, à 3 tokens par seconde sans carte graphique.
- **macOS avec un vrai modèle.** La pipeline y fait tourner le moteur, le bac à sable et un bloc de Llama 3.2 3B, pas une chaîne complète.
- **Une semaine de fonctionnement.** Le plus long essai a duré une heure.

## Tester la détection d'un tricheur

Sur une machine qui porte des blocs, ferme Tor6IA puis relance-le avec la variable `T6E_TEST_NOISE=1` (sous Windows, dans un terminal : `set T6E_TEST_NOISE=1`, puis lance `%LOCALAPPDATA%\Tor6IA\tor6ia.exe`). Cette machine renvoie alors du bruit à la place de ses calculs. Si une autre machine porte les mêmes blocs, le réseau doit l'écarter en quelques minutes : la liste des modèles perd un porteur pour ses blocs, et les réponses restent correctes. L'exclusion dure 24 heures.

## Limites du relais public

Le relais tourne sur un serveur partagé. Il est plafonné à 16 Mbit/s par paire de machines, 24 Mbit/s en tout et 20 Go par jour. Charger un modèle fait passer quelques mégaoctets ; chaque token en fait passer entre 350 et 700 Ko selon le nombre de machines dans la chaîne. À 3 tokens par seconde, une chaîne de 4 machines toutes jointes par le relais consomme environ 1,4 Mo par seconde : le plafond journalier est atteint après 4 heures de réponses continues. Pour un essai plus long, dis-le-moi : les plafonds se changent dans `deploy/remote.sh`.

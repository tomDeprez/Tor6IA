# Tor6IA : partage ta machine, utilise de plus gros modèles d'IA

[English](README.md)

L'IA en commun. Tu prêtes ta machine pour calculer, tu gagnes des crédits. Tu dépenses ces crédits pour utiliser les modèles que font tourner les autres machines, y compris ceux trop gros pour la tienne.

- **Un seul fichier.** `tor6ia.exe` installe Ollama, choisit le modèle adapté à ta machine, le télécharge et ouvre l'interface.
- **Zéro compte.** Ton identité est une clé créée sur ta machine. Pas d'e-mail, pas de mot de passe.
- **Équitable.** Aucun crédit n'est jamais créé. Tu gagnes exactement ce qu'une autre personne paie pour le calcul fait sur ta machine.
- **Vérifiable.** Le registre des crédits est public, chaîné et signé. Chaque nœud le vérifie tout seul.
- **Chiffré de bout en bout.** Le coordinateur sait qui demande mais ne peut pas lire. La machine qui répond lit le message mais ne sait pas qui l'envoie.
- **API compatible OpenAI** sur `http://localhost:7606/v1` : tes outils habituels fonctionnent.

## Installer

### Windows

**[Télécharge tor6ia-windows-amd64.exe](https://github.com/tomDeprez/Tor6IA/releases/latest/download/tor6ia-windows-amd64.exe)** et double-clique dessus.

Il s'installe tout seul, installe Ollama, choisit le modèle adapté à ta machine, le télécharge et ouvre l'interface. Sans droits administrateur.

Windows affiche « Windows a protégé votre ordinateur » car le fichier n'est pas encore signé : clique sur **Informations complémentaires**, puis **Exécuter quand même**. Ou passe par PowerShell, sans avertissement :

```powershell
irm https://raw.githubusercontent.com/tomDeprez/Tor6IA/main/install.ps1 | iex
```

### Linux et macOS

```sh
curl -fsSL https://raw.githubusercontent.com/tomDeprez/Tor6IA/main/install.sh | sh
```

L'interface s'ouvre sur `http://localhost:7606`.

Désinstaller sous Windows : télécharge `install.ps1` puis `.\install.ps1 -Uninstall`. Ton compte et tes crédits restent dans `%APPDATA%\Tor6IA`.

## L'interface

Le gros bouton en haut dit ce que fait ta machine. Un clic met le partage en pause ou le reprend.

| Couleur | État |
|---|---|
| Vert | Connecté, en attente d'un calcul |
| Bleu | Calcul en cours pour quelqu'un : tu gagnes des crédits |
| Orange | Installation ou connexion en cours |
| Gris | En pause, ou coordinateur injoignable |
| Rouge | Problème, ou alerte : le registre a échoué à l'audit |

La liste des modèles montre combien de machines proposent chacun, son prix, et combien de tokens ton solde permet. **Partager** propose un modèle de plus depuis ta machine. Le bouton demande toujours confirmation et dit ce que ça implique : taille du téléchargement, vitesse sur ton matériel, ce que tu gagnes.

**Partagé ✓** arrête de partager un modèle, et peut supprimer ses fichiers pour libérer le disque. Un téléchargement en cours s'annule. **Désinstaller Tor6IA…**, en bas, retire tout : la machine quitte le réseau, les modèles partagés et le programme sont supprimés. Quand une machine se met en pause ou s'en va, les autres voient aussitôt qu'il manque une machine pour ses modèles.

**Mises à jour.** Quand une nouvelle version est publiée, le programme montre ce qui change et se met à jour en un clic. Une version compilée depuis le code ne demande jamais.

## Les modèles

| Modèle | Prix | Rapide sur | Lent sur |
|---|---|---|---|
| Llama 3.2 3B | ×1 | n'importe quel PC | |
| Gemma 3 12B | ×4 | carte graphique 10 Go | processeur avec 13 Go de mémoire |
| Gemma 3 27B | ×9 | carte graphique 20 Go | processeur avec 25 Go de mémoire |
| Llama 3.3 70B | ×23 | 46 Go de mémoire graphique | processeur avec 58 Go de mémoire |

Un nœud démarre avec le plus gros modèle que tient sa carte graphique. Une machine ne charge jamais un modèle pour lequel elle n'a pas la mémoire, et le calcul n'utilise que la moitié de ses processeurs : elle reste utilisable. Seules les cartes NVIDIA et les Mac Apple Silicon sont détectés automatiquement.

Un modèle sans machine affiche « en attente d'une machine ». Il devient disponible dès que quelqu'un d'assez équipé le partage.

## Les crédits

**Aucun crédit n'est créé.** Un paiement déplace des crédits du compte qui demande vers le compte qui calcule. La somme de tous les soldes vaut toujours zéro.

- **Tout le monde part de 0.**
- **Gagner.** Quand une autre personne calcule sur ta machine, tu reçois exactement ce qu'elle paie.
- **Prix.** Un crédit vaut environ un token d'un modèle ×1 : 4 octets de réponse, ou 40 octets de question, multipliés par le prix du modèle. Le coordinateur compte les octets, ton nœud recompte et alerte en cas d'écart.
- **Découvert de départ.** Si tout le monde est à 0, personne ne peut payer la première demande. Une machine qui réussit un calcul de test (le coordinateur en connaît la réponse) peut donc descendre jusqu'à −200. Ce test est limité à une machine par adresse IP toutes les 10 minutes.
- **Gratuit chez soi.** Le modèle que ta machine héberge répond en local, sans crédit et sans réseau.
- **Plafond.** Le coût maximal est bloqué avant le calcul, la réponse s'arrête quand la limite est atteinte.
- **Bascule automatique.** Si la machine qui calcule disparaît avant de répondre, ton nœud en demande une autre. L'essai raté ne coûte rien.

## Impossible de tricher sur les crédits

Le registre est public : `https://<coordinateur>/v1/ledger`. Chaque ligne contient le hachage de la précédente, comme une blockchain.

| Règle | Ce qui la garantit |
|---|---|
| Personne ne prend tes crédits, pas même le coordinateur | Un paiement n'est valide qu'avec la signature de ta clé, pour ce job, ce bénéficiaire et ce plafond |
| Aucun crédit ne sort de nulle part | Le registre ne connaît que des paiements : la somme reste nulle |
| Personne ne dépense plus que son solde et son découvert | Vérifié à chaque ligne |
| Le passé ne peut pas être réécrit | Chaque ligne est chaînée et signée ; chaque nœud garde la dernière position qu'il a vérifiée |

Chaque nœud rejoue tout le registre au démarrage puis toutes les 30 minutes. Une règle violée affiche une alerte rouge. Pour vérifier à la main :

```sh
tor6ia audit
```

## Créer un réseau

Sans coordinateur configuré, le nœud tourne en **mode solo** : il lance un coordinateur privé, limité à la machine. Un réseau partagé a besoin d'une machine joignable par tous, qui fait tourner le coordinateur. Il ne calcule rien : un petit serveur suffit.

Avec un serveur Debian ou Ubuntu, Apache et certbot, et un accès SSH par clé :

```powershell
./deploy.ps1 -Server root@203.0.113.10 -Domain tor6ia.example.org
```

Le script installe un service `tor6ia-coordinator` qui sert HTTPS lui-même sur le port 8443, avec son utilisateur, des limites de mémoire et de processeur, et un certificat Let's Encrypt renouvelé automatiquement. Il ne modifie aucun site existant. Relance la même commande pour mettre à jour.

Sans nom de domaine, `tor6ia.203-0-113-10.sslip.io` désigne l'adresse `203.0.113.10`.

Derrière ton propre proxy HTTPS à la place : `tor6ia coordinator --addr 127.0.0.1:8787 --trust-proxy`.

Compile ensuite les clients pour ton réseau :

```powershell
./build.ps1 -Coordinator https://tor6ia.example.org:8443
```

## Pipeline

`.github/workflows/ci.yml` :

- **À chaque push** : format, `go vet`, tests.
- **À chaque tag `vX.Y.Z`** : déploiement du coordinateur, publication des binaires dans une release GitHub, puis installation en une commande sur des machines Linux et macOS vierges, avec une question posée au modèle.

```sh
git tag v0.2.0 && git push origin v0.2.0
```

Les notes de version sont la section `## v0.2.0` de `CHANGELOG.md`, à écrire avant le tag. Les programmes installés les affichent dans leur fenêtre de mise à jour.

Configuration du dépôt (Settings, Secrets and variables, Actions) :

| Nom | Type | Rôle |
|---|---|---|
| `COORDINATOR_URL` | variable | réseau que rejoignent les clients publiés |
| `DEPLOY_SERVER` | variable | `root@adresse`. Vide : pas de déploiement automatique |
| `DEPLOY_DOMAIN` | variable | nom du coordinateur |
| `DEPLOY_KNOWN_HOSTS` | variable | sortie de `ssh-keyscan adresse` |
| `DEPLOY_SSH_KEY` | secret | clé privée dédiée au déploiement |

Le déploiement automatique donne à GitHub Actions un accès root au serveur. Utilise une clé dédiée, jamais ta clé personnelle. Sans ces réglages, déploie à la main avec `deploy.ps1`.

## Utiliser l'API

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:7606/v1", api_key="unused")
reply = client.chat.completions.create(
    model="llama3.2:3b",
    messages=[{"role": "user", "content": "Bonjour !"}],
)
print(reply.choices[0].message.content)
```

Seul le texte est accepté : pas d'images, pas d'appels d'outils.

## Comment ça marche

```
toi ── question chiffrée ──▶ coordinateur ── question chiffrée ──▶ machine qui calcule
toi ◀── réponse chiffrée ─── coordinateur ◀── réponse chiffrée ─── (Ollama local)
```

1. Ton nœud demande un modèle. Le coordinateur réserve la machine libre la plus rapide et renvoie sa clé publique.
2. Ton nœud chiffre la conversation pour cette machine (X25519, AES-256-GCM) et signe le prix maximal.
3. La machine déchiffre, calcule, renvoie la réponse chiffrée par morceaux.
4. Le coordinateur compte les octets et inscrit le paiement signé dans le registre.

Les nœuds ne reçoivent aucune connexion entrante : ils interrogent le coordinateur. Pas de port à ouvrir, ça marche derrière une box.

## Limites connues

- **Un modèle tourne en entier sur une seule machine.** Tor6IA ne découpe pas encore un gros modèle entre plusieurs petites machines. Un modèle est disponible quand au moins une machine peut le tenir.
- **La machine qui calcule lit ta question.** Elle ne sait pas de qui elle vient, mais n'envoie rien de secret.
- **Le coordinateur reste un point de confiance pour trois choses.** Il attribue les machines : un coordinateur malveillant pourrait se faire passer pour une machine et lire les messages. Il compte les octets des réponses : ton nœud recompte ce qu'il paie, pas encore ce qu'il gagne. Il accorde les découverts : il pourrait en accorder à de faux comptes, mais chacun est plafonné à 200 et visible dans le registre.
- **Un compte peut partir avec sa dette.** Le découvert de 200 est le coût d'entrée du réseau. Le test par adresse IP le limite, plusieurs adresses le contournent.
- **Le modèle annoncé n'est pas prouvé.** Les tests vérifient qu'une machine calcule vraiment, pas qu'elle fait tourner le modèle qu'elle annonce. Trois tests ratés excluent la machine une heure.
- **Le registre est public.** Les comptes sont des clés anonymes, mais qui paie qui, et combien, se lit.
- **Un job à la fois par machine.** Les demandes attendent 30 secondes au plus une machine libre.

## Licence

[MIT](LICENSE)

## Développer

```sh
go test ./...
go build -o dist/tor6ia.exe .
```

Aucune dépendance hors de la bibliothèque standard de Go.

| Fichier | Rôle |
|---|---|
| `proto.go` | messages, signatures, chiffrement, prix |
| `ledger.go` | registre public : règles, chaîne, audit |
| `coordinator.go` | file des jobs, tests, règlement |
| `node.go` | calcul pour le réseau, API locale, audit |
| `ollama.go` | installation d'Ollama, modèles, détection du matériel |
| `ui/index.html` | interface |
| `deploy.ps1`, `deploy/remote.sh` | déploiement du coordinateur |

# Tor6IA: share your computer, use bigger AI models

[![CI](https://github.com/tomDeprez/Tor6IA/actions/workflows/ci.yml/badge.svg)](https://github.com/tomDeprez/Tor6IA/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/tomDeprez/Tor6IA)](https://github.com/tomDeprez/Tor6IA/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/tomDeprez/Tor6IA/total)](https://github.com/tomDeprez/Tor6IA/releases)

**Tor6IA is a peer-to-peer network for running open LLMs.** Your machine runs a model for other people and earns credits. You spend those credits on the models other machines run, including models too large for your own computer.

One file to install. No account. End-to-end encrypted. OpenAI-compatible API. Built on [Ollama](https://ollama.com).

[Français](README.fr.md)

## Install

### Windows

**[Download tor6ia-windows-amd64.exe](https://github.com/tomDeprez/Tor6IA/releases/latest/download/tor6ia-windows-amd64.exe)** and double-click it.

It installs itself, installs Ollama, picks the model that fits your machine, downloads it and opens the interface. No admin rights needed.

Windows shows "Windows protected your PC" because the file is not signed yet: click **More info**, then **Run anyway**. Or use PowerShell, which shows no warning:

```powershell
irm https://raw.githubusercontent.com/tomDeprez/Tor6IA/main/install.ps1 | iex
```

### Linux and macOS

```sh
curl -fsSL https://raw.githubusercontent.com/tomDeprez/Tor6IA/main/install.sh | sh
```

The interface opens on `http://localhost:7606`.

## Why

Running a large language model at home needs a graphics card most people do not have. Tor6IA pools machines:

- **Fair.** No credit is ever created. You earn exactly what another person pays for the work done on your machine.
- **Verifiable.** The credit ledger is public, hash-chained and signed, like a blockchain. Every node audits it.
- **Private.** Prompts and answers are encrypted end to end. The coordinator knows who asks but cannot read. The machine that answers reads the prompt but does not know who sent it.
- **No account.** Your identity is a key created on your machine. No e-mail, no password.
- **Works with your tools.** OpenAI-compatible API on `http://localhost:7606/v1`.

## The interface

The large button at the top says what your machine is doing. Click it to pause or resume sharing.

| Colour | State |
|---|---|
| Green | Connected, waiting for a job |
| Blue | Computing for someone: you earn credits |
| Orange | Installing or connecting |
| Grey | Paused, or coordinator unreachable |
| Red | Problem, or alert: the ledger failed its audit |

The model list shows how many machines offer each model, its price, and how many tokens your balance buys. **Share** offers one more model from your machine. It always asks first and says what it implies: download size, speed on your hardware, what you earn.

## Models

| Model | Price | Runs fast on | Runs slowly on |
|---|---|---|---|
| Llama 3.2 3B | ×1 | any PC | |
| Gemma 3 12B | ×4 | 10 GB graphics card | processor with 13 GB of memory |
| Gemma 3 27B | ×9 | 20 GB graphics card | processor with 25 GB of memory |
| Llama 3.3 70B | ×23 | 46 GB of graphics memory | processor with 58 GB of memory |

A node starts with the largest model its graphics card holds. A machine never loads a model it has no memory for, and computing uses half of its processors, so it stays usable. Only NVIDIA cards and Apple Silicon are detected automatically.

A model with no machine shows "waiting for a machine". It becomes available the moment someone with enough hardware shares it.

## Credits

**No credit is created.** A payment moves credits from the account that asks to the account that computes. All balances always add up to zero.

- **Everyone starts at 0.**
- **Earning.** When another person computes on your machine, you receive exactly what they pay.
- **Price.** One credit is about one token of a ×1 model: 4 bytes of answer, or 40 bytes of prompt, times the model price. The coordinator counts the bytes; your node counts them again and raises an alert on any difference.
- **Starting overdraft.** If everyone is at 0, nobody can pay for the first request. So a machine that passes a test job, whose answer the coordinator knows, may go down to −200. The test is limited to one machine per IP address every 10 minutes.
- **Free at home.** A model your own machine hosts answers locally, with no credit and no network.
- **Failover.** If the machine that computes goes away before answering, your node asks another one. The failed attempt costs nothing.

## Nobody can cheat on credits

The ledger is public: `https://<coordinator>/v1/ledger`. Each line contains the hash of the previous one.

| Rule | What enforces it |
|---|---|
| Nobody takes your credits, not even the coordinator | A payment is valid only with your key's signature, for that job, that payee and that ceiling |
| No credit comes out of nowhere | The ledger only knows payments: the sum stays zero |
| Nobody spends more than balance plus overdraft | Checked on every line |
| The past cannot be rewritten | Every line is chained and signed; every node keeps the last position it verified |

Every node replays the whole ledger at startup, then every 30 minutes. A broken rule shows a red alert. To check by hand:

```sh
tor6ia audit
```

## Use the API

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:7606/v1", api_key="unused")
reply = client.chat.completions.create(
    model="llama3.2:3b",
    messages=[{"role": "user", "content": "Hello!"}],
)
print(reply.choices[0].message.content)
```

Text only: no images, no tool calls.

## How it works

```
you ── encrypted prompt ──▶ coordinator ── encrypted prompt ──▶ machine that computes
you ◀── encrypted answer ── coordinator ◀── encrypted answer ── (local Ollama)
```

1. Your node asks for a model. The coordinator reserves the fastest free machine and returns its public key.
2. Your node encrypts the conversation for that machine (X25519, AES-256-GCM) and signs the highest price it accepts.
3. The machine decrypts, computes, and sends the answer back encrypted, piece by piece.
4. The coordinator counts the bytes and writes the signed payment to the ledger.

Nodes accept no incoming connection: they poll the coordinator. No port to open, it works behind a home router.

## Run your own network

Without a coordinator, a node runs in **solo mode**: a private coordinator limited to the machine. A shared network needs one machine everybody can reach. It computes nothing: a small server is enough.

With a Debian or Ubuntu server, Apache, certbot and SSH key access:

```powershell
./deploy.ps1 -Server root@203.0.113.10 -Domain tor6ia.example.org
```

The script installs a `tor6ia-coordinator` service that serves HTTPS itself on port 8443, with its own user, memory and processor limits, and a Let's Encrypt certificate renewed automatically. It changes no existing site. Run it again to update.

Without a domain name, `tor6ia.203-0-113-10.sslip.io` points to `203.0.113.10`.

Then build the clients for your network:

```powershell
./build.ps1 -Coordinator https://tor6ia.example.org:8443
```

## Pipeline

`.github/workflows/ci.yml`:

- **Every push**: format, `go vet`, tests.
- **Every tag `vX.Y.Z`**: deploys the coordinator, publishes the binaries as a GitHub release, then runs the one-line install on clean Linux and macOS machines and asks the model a question.

```sh
git tag v0.2.0 && git push origin v0.2.0
```

Repository settings (Settings, Secrets and variables, Actions):

| Name | Kind | Role |
|---|---|---|
| `COORDINATOR_URL` | variable | network the published clients join |
| `DEPLOY_SERVER` | variable | `root@address`. Empty: no automatic deploy |
| `DEPLOY_DOMAIN` | variable | name of the coordinator |
| `DEPLOY_KNOWN_HOSTS` | variable | output of `ssh-keyscan address` |
| `DEPLOY_SSH_KEY` | secret | private key used only for deploys |

Automatic deploy gives GitHub Actions root access to the server. Use a dedicated key, never your personal one. Without these settings, deploy by hand with `deploy.ps1`.

## Known limits

- **One model runs whole on one machine.** Tor6IA does not yet split a large model across several small machines. A model is available when at least one machine can hold it.
- **The machine that computes reads your prompt.** It does not know who you are, but do not send secrets.
- **The coordinator is still trusted for three things.** It assigns machines: a malicious coordinator could pose as a machine and read messages. It counts answer bytes: your node recounts what it pays, not yet what it earns. It grants overdrafts: it could grant them to fake accounts, but each is capped at 200 and visible in the ledger.
- **An account can leave with its debt.** The 200 overdraft is the entry cost of the network. The per-IP test limits it; several addresses get around it.
- **The announced model is not proven.** Tests check that a machine really computes, not which model it runs. Three failed tests exclude it for an hour.
- **The ledger is public.** Accounts are anonymous keys, but who pays whom, and how much, is readable.
- **One job at a time per machine.** Requests wait at most 30 seconds for a free machine.

## Develop

```sh
go test ./...
go build -o dist/tor6ia.exe .
```

No dependency outside the Go standard library.

| File | Role |
|---|---|
| `proto.go` | messages, signatures, encryption, prices |
| `ledger.go` | public ledger: rules, chain, audit |
| `coordinator.go` | job queue, test jobs, settlement |
| `node.go` | computing for the network, local API, audit |
| `ollama.go` | Ollama install, models, hardware detection |
| `ui/index.html` | interface |
| `deploy.ps1`, `deploy/remote.sh` | coordinator deployment |

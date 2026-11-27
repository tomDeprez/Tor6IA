#!/bin/bash
# Runs on the server as root, called by deploy.ps1. Installs or updates the Tor6IA coordinator.
# It only adds its own files, user and service: no existing site or service is modified.
# The coordinator serves HTTPS itself on its own port, so its many long-lived connections
# never use the web server's workers.
set -euo pipefail
DOMAIN=$1 PORT=$2 NEW=$3

if ss -ltnpH "sport = :$PORT" | grep -v '"tor6ia"' | grep -q .; then
  echo "port $PORT is already used by another program" >&2
  exit 1
fi

id -u tor6ia >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin tor6ia
install -d -m 755 /opt/tor6ia
install -d -o tor6ia -g tor6ia -m 700 /var/lib/tor6ia
# Copy then rename: the swap is atomic and works while the old binary runs.
install -m 755 "$NEW" /opt/tor6ia/tor6ia.new
mv -f /opt/tor6ia/tor6ia.new /opt/tor6ia/tor6ia

cat > /opt/tor6ia/cert-hook.sh <<EOF
#!/bin/sh
# Certbot runs this at each renewal. The coordinator is not root, so it gets its own copy of the certificate.
set -e
install -d -o tor6ia -g tor6ia -m 700 /var/lib/tor6ia/tls
install -o tor6ia -g tor6ia -m 600 /etc/letsencrypt/live/$DOMAIN/fullchain.pem /etc/letsencrypt/live/$DOMAIN/privkey.pem /var/lib/tor6ia/tls/
systemctl try-restart tor6ia-coordinator
EOF
chmod 755 /opt/tor6ia/cert-hook.sh

cat > /etc/systemd/system/tor6ia-coordinator.service <<EOF
[Unit]
Description=Tor6IA coordinator
After=network-online.target
Wants=network-online.target

[Service]
User=tor6ia
ExecStart=/opt/tor6ia/tor6ia coordinator --addr :$PORT --data /var/lib/tor6ia --tls-cert /var/lib/tor6ia/tls/fullchain.pem --tls-key /var/lib/tor6ia/tls/privkey.pem
Restart=always
RestartSec=2
LimitNOFILE=65536

# Limits: whatever happens to Tor6IA, the other services keep their memory and CPU.
MemoryMax=512M
CPUQuota=100%
Nice=5

# Sandbox: the service can write its data directory and nothing else.
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/tor6ia
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_INET6
LockPersonality=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --quiet tor6ia-coordinator

if [ ! -e "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" ]; then
  # Same method as the certificates already on this server: the web server answers the challenge.
  certbot certonly --apache -d "$DOMAIN" --non-interactive --deploy-hook /opt/tor6ia/cert-hook.sh
fi

/opt/tor6ia/cert-hook.sh # refreshes the certificate copy and restarts a running coordinator on the new binary
systemctl is-active --quiet tor6ia-coordinator || systemctl start tor6ia-coordinator

for _ in 1 2 3 4 5 6 7 8 9 10; do
  if curl -fs --max-time 5 --resolve "$DOMAIN:$PORT:127.0.0.1" "https://$DOMAIN:$PORT/"; then
    exit 0
  fi
  sleep 1
done
echo "the coordinator does not answer:" >&2
journalctl -u tor6ia-coordinator -n 20 --no-pager >&2
exit 1

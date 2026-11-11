# Base sandbox image: toolchains, sshd and sandbox-init. Built by scripts/publish-images.sh.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates python3 git openssh-server curl netcat-openbsd build-essential procps less nano \
    && rm -rf /var/lib/apt/lists/* \
    && rm -f /etc/ssh/ssh_host_* && ssh-keygen -q -t ed25519 -N '' -f /etc/ssh/ssh_host_ed25519_key
COPY sshd_config /etc/ssh/sshd_config
COPY sandbox-init abx-shell /usr/local/bin/
COPY sample-repo /opt/sample-repo
RUN chmod 755 /usr/local/bin/sandbox-init /usr/local/bin/abx-shell \
    && echo /usr/local/bin/abx-shell >> /etc/shells \
    && sed -i 's#^root:\(.*\):/bin/bash$#root:\1:/usr/local/bin/abx-shell#' /etc/passwd \
    && mkdir -p /etc/agentbox /workspace /run/sshd \
    && printf 'set -a\nwhile IFS= read -r l; do [ -n "$l" ] && export "$l"; done < /etc/agentbox/env 2>/dev/null\nset +a\n' > /etc/profile.d/agentbox.sh
ENV LANG=C.UTF-8

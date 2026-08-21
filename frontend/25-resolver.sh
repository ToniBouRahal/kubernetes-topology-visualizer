#!/bin/sh
# Substitute the cluster's DNS servers into the rendered nginx config.
#
# The image exports NGINX_LOCAL_RESOLVERS, but it does not survive into envsubst's variable list
# here, so ${NGINX_LOCAL_RESOLVERS} reached nginx literally and it refused to start. Reading
# /etc/resolv.conf directly is deterministic and works on any cluster, without depending on the
# base image's substitution behaviour.
#
# Runs after 20-envsubst-on-templates.sh, so it edits the already-rendered config.
set -eu

conf=/etc/nginx/conf.d/default.conf
[ -f "$conf" ] || exit 0

resolvers=$(awk '/^nameserver/ { printf "%s ", $2 }' /etc/resolv.conf)
if [ -z "$resolvers" ]; then
    echo "25-resolver.sh: no nameserver in /etc/resolv.conf; dropping the resolver directive" >&2
    # Without a resolver a variable proxy_pass cannot resolve at all, so fall back to nginx's
    # start-up resolution rather than serving a config that fails every request.
    sed -i '/__RESOLVERS__/d' "$conf"
    sed -i 's|set \$backend "\(.*\)";|set $backend "\1";|' "$conf"
    exit 0
fi

echo "25-resolver.sh: using resolvers: $resolvers"
sed -i "s|__RESOLVERS__|${resolvers}|" "$conf"

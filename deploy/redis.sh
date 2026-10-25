#!/bin/sh
set -eu
umask 077
printf 'appendonly yes\nrequirepass %s\n' "$(cat /run/redis/redis_password)" > /tmp/redis.conf
chown redis:redis /tmp/redis.conf
exec /usr/local/bin/docker-entrypoint.sh redis-server /tmp/redis.conf

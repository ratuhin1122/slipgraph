#!/bin/sh
set -eu
umask 077
for name in postgres owner app redis; do
  target="/generated/$name/${name}_password"
  if [ ! -s "$target" ]; then
    od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > "$target.tmp"
    [ "$(wc -c < "$target.tmp")" -eq 64 ]
    mv "$target.tmp" "$target"
  fi
  chmod 0444 "$target"
done

target=/generated/app/auth_key
if [ ! -s "$target" ]; then
 od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > "$target.tmp"
 [ "$(wc -c < "$target.tmp")" -eq 64 ]
 mv "$target.tmp" "$target"
fi
chmod 0444 "$target"

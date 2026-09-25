#!/bin/sh
# Entrypoint for the API image: make sure the Python dependencies exist, then exec the command.
#
# torch and the rest of requirements.lock.txt are NOT in the image. Unpacked they are ~2 GB, and as an
# image layer they were rebuilt and re-pulled onto k8s-5's 48 GB root disk whenever the lock changed,
# next to the previous copy, which kept tipping the node into DiskPressure. Instead they go into a venv
# under $PHOTOSORT_PYDEPS (on the models PVC in the cluster), one per lock hash:
#
#   $PHOTOSORT_PYDEPS/<key>/       venv for one lock; <key> hashes the lock, torch index, python, this script
#   $PHOTOSORT_PYDEPS/.pip-cache/  wheel cache, so a lock change only downloads the packages that changed
#   $PHOTOSORT_PYDEPS/.tmp-*       an install in progress; renamed into place only once it imports
#
# First start after a lock change installs from PyPI + $PHOTOSORT_TORCH_INDEX (needs egress, a few
# minutes; the pod just isn't Ready meanwhile). Every other start is a hash and an exec.
set -eu

root="${PHOTOSORT_PYDEPS:-/app/.pydeps}"
torch_index="${PHOTOSORT_TORCH_INDEX:-https://download.pytorch.org/whl/cpu}"
lock="${PHOTOSORT_LOCK:-/app/requirements.lock.txt}"
keep=3   # envs kept: the current one, the one a still-running old pod may use, one for a rollback

log() { echo "pydeps: $*" >&2; }

[ -f "$lock" ] && [ -r "$lock" ] || { log "can't read lock file $lock"; exit 1; }

key=$({ python -c 'import platform, sys; print(sys.version_info[:2], platform.machine())'
        echo "$torch_index"; cat "$lock" "$0"; } | sha256sum | cut -c1-16)
env="$root/$key"

if [ ! -f "$env/.ready" ]; then
    mkdir -p "$root"
    export PIP_CACHE_DIR="${PIP_CACHE_DIR:-$root/.pip-cache}" PIP_DISABLE_PIP_VERSION_CHECK=1
    tmp="$root/.tmp-$key-$(hostname)"
    log "installing $key into $root (lock changed or first start)"
    rm -rf "$tmp"
    python -m venv "$tmp"
    # torch/torchvision are pinned by the lock but come from the torch index (CPU build by default);
    # everything else installs --no-deps since the lock is a full closure, which also keeps
    # ultralytics from pulling the GUI opencv-python in next to the headless one (they share cv2/).
    torch_reqs=$(grep -E '^(torch|torchvision)==' "$lock" || true)
    if [ -n "$torch_reqs" ]; then
        # shellcheck disable=SC2086
        "$tmp/bin/python" -m pip install --no-deps $torch_reqs --index-url "$torch_index"
    fi
    grep -vE '^(torch|torchvision|opencv-python)==|^-e ' "$lock" > "$tmp/requirements.txt"
    "$tmp/bin/python" -m pip install --no-deps -r "$tmp/requirements.txt"
    "$tmp/bin/python" -c "import cv2, torch, ultralytics, fastapi; print('cv2', cv2.__version__, 'torch', torch.__version__)"
    touch "$tmp/.ready"
    # Another pod may have finished the same key first; theirs is just as good.
    if [ -f "$env/.ready" ]; then rm -rf "$tmp"; else rm -rf "$env"; mv -T "$tmp" "$env"; fi
    log "installed $key"
fi

# Mark this env as in use, then drop all but the $keep most recently used, and stale partial installs.
touch "$env"
ls -1dt "$root"/*/ 2>/dev/null | tail -n +$((keep + 1)) | while read -r old; do
    [ -f "$old/.ready" ] && [ "${old%/}" != "$env" ] && { log "removing unused ${old%/}"; rm -rf "$old"; }
done
find "$root" -mindepth 1 -maxdepth 1 -name '.tmp-*' -mmin +120 -exec rm -rf {} + 2>/dev/null || true

export VIRTUAL_ENV="$env" PATH="$env/bin:$PATH"
exec "$@"

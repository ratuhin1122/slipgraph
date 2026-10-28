"""Encode actual UI screenshots into the README GIF with the built FFmpeg image.

No browser replay is fabricated here: capture the real paused demo first, using
viewport PNGs named .showcase/frames/frame001.png, frame002.png, ... .
"""
from pathlib import Path
import re
import subprocess
from showcase_client import PRIVATE, ROOT

frames = PRIVATE / "frames"
if PRIVATE.is_symlink() or not PRIVATE.resolve().is_relative_to(ROOT.resolve()) or not frames.resolve().is_relative_to(PRIVATE.resolve()) or frames.is_symlink():
    raise SystemExit("Frames must stay in the private showcase directory")
names = sorted(p.name for p in frames.glob("frame*.png") if re.fullmatch(r"frame[0-9]{3}\.png", p.name))
if not 3 <= len(names) <= 20:
    raise SystemExit("Capture 3–20 real viewport screenshots before encoding")
for name in names:
    if (frames / name).is_symlink():
        raise SystemExit("Screenshot symlinks are not accepted")
concat = "".join(f"file '{name}'\nduration 3\n" for name in names) + f"file '{names[-1]}'\n"
(frames / "frames.txt").write_text(concat, encoding="ascii")
output = ROOT / "docs" / "assets"
if output.is_symlink() or not output.resolve().is_relative_to(ROOT.resolve()):
    raise SystemExit("Output must remain inside the repository")
output.mkdir(exist_ok=True)
container = subprocess.check_output(["docker", "compose", "ps", "-q", "api"], text=True).strip()
if not container or "\n" in container:
    raise SystemExit("Start one Compose API container first")
image = subprocess.check_output(["docker", "inspect", "--format", "{{.Config.Image}}", container], text=True).strip()
if not image or "\n" in image:
    raise SystemExit("Start the Compose API image first")
subprocess.run(["docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--tmpfs", "/tmp", "--mount", f"type=bind,source={frames.resolve()},target=/frames,readonly", "--mount", f"type=bind,source={output.resolve()},target=/output", "--entrypoint", "ffmpeg", image, "-hide_banner", "-loglevel", "error", "-y", "-f", "concat", "-safe", "1", "-i", "/frames/frames.txt", "-filter_complex", "fps=3,scale=trunc(min(iw\\,1100)/2)*2:-2:flags=lanczos,split[a][b];[a]palettegen=max_colors=128[p];[b][p]paletteuse=dither=bayer:bayer_scale=3", "-loop", "0", "/output/delay-demo.gif"], check=True, timeout=180)
assert (output / "delay-demo.gif").stat().st_size > 10000
print("Encoded docs/assets/delay-demo.gif from actual UI screenshots.")

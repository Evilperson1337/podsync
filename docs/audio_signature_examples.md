# Audio Signature Examples (Windows)

These examples assume:

- Input: `bin\input.mp3`
- Signature audio: `bin\signature.wav`

## Build

```cmd
bin\build_audiosplitdetect.cmd
```

## Basic detection (audio file)

```cmd
.\bin\audiosplitdetect.exe /in:.\bin\input.mp3 /sig-audio:.\bin\signature.wav
```

## Trim output (re-encode, bitrate preserved)

```cmd
.\bin\audiosplitdetect.exe /in:.\bin\input.mp3 /sig-audio:.\bin\signature.wav /trim /out:.\bin\output.mp3
```

## Trim output (stream copy)

```cmd
.\bin\audiosplitdetect.exe /in:.\bin\input.mp3 /sig-audio:.\bin\signature.wav /trim /copy /out:.\bin\output.mp3
```


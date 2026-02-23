# OcrBoard

A lightweight global hotkey OCR tool for Windows.

Select any region on screen and instantly send it to an OCR server.

OcrBoard uses [macocr](https://github.com/riddleling/macocr) as the backend OCR server.


## Features

- Global hotkey: Win + Alt + Shift + T
- Region selection with dimmed overlay
- ESC to cancel selection
- Automatically copies OCR result to clipboard
- Displays OCR result in a message box
- Shows API response time in console
- Supports custom server IP / port


## How It Works

1. Press Win + Alt + Shift + T
2. Drag to select a region
3. Image is sent to the OCR server
4. OCR result:
    - Copied to clipboard
    - Displayed in a popup

Backend OCR is powered by:
👉 [macocr](https://github.com/riddleling/macocr)


## Requirements

### Client (OcrBoard)

- Windows 10 or later
- Go 1.20+ (for building from source)

### Server

You must run macocr as the OCR server.

macocr repository:
```
https://github.com/riddleling/macocr
```

Start macocr server (example):
```
macocr -s -p 8000
```

## Running OcrBoard

```
OcrBoard.exe -ip 10.0.1.13 -port 8000
```

Or create a `.bat` file:
```
@echo off
cd /d "%~dp0"
OcrBoard.exe -ip 10.0.1.13 -port 8000
```

## Command Line Options

| Option  | Description                     | Default     |
| ------- | ------------------------------- | ----------- |
| `-ip`   | OCR server IP                   | `127.0.0.1` |
| `-port` | OCR server port                 | `8000`      |
| `-path` | API path                        | `/upload`   |
| `-url`  | Full API URL (overrides others) | —           |


## Build From Source

```
git clone https://github.com/riddleling/OcrBoard.git
cd OcrBoard
go build
```

## Notes

- Make sure macocr server is running before triggering the hotkey.
- If hotkey does not respond, check:
    - Another application is not using the same key combination
    - Windows accessibility features are not intercepting the shortcut


## License

MIT License

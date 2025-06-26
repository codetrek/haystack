## Setup env
- Run `npm install` in this dir
- Build mcp-tool to receive task result from GHC.
  - cd `src\mcp` and run `tsc`
  - add this mcp tool to vscode
- Hack vscode Copilot extension.
  - Open `C:\Users\<UserName>\.vscode\extensions\github.copilot-chat-0.28.2\dist\extension.js
  - Add this code behind `r.microsoft&&this._microsoftTelemetrySender.sendTelemetryEvent(e,n,o)`
  ```
  ;
  if (e == 'panel.request' || e == 'toolCallDetails') {
    const ff = require("fs");
    const logMessage = JSON.stringify({'eventname': e, 'properties': n, 'measurements': o});
    ff.appendFileSync("Q:\\log.txt", logMessage+'\n', { encoding: "utf-8" });
  }
  ```
- Open i11y and build it via vscode studio 2022

- Update config.json
```
{
  "workspace": "...",
  "vscode_log": "Q:/log.txt",
  "a11y_exe": ".../i11y/x64/Release/i11y.exe"
}

```

## Run Benchmark
- Start vscode and open Edge repo.
- Run `tsx .\src\e2e.ts`.
- Benchmark report (See .\src\e2e.ts `report` function).
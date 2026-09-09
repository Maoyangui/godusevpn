package ipc

// macOS 没有 /run,运行时目录是 /var/run(指向 /private/var/run)。
const runDir = "/var/run"

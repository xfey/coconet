// Keep setup feedback on stderr so the original command retains its stdout
// protocol. Redirected output gets one line per phase, never animation.
export class SetupProgress {
  constructor(output = process.stderr, environment = process.env) {
    this.output = output;
    this.interactive = Boolean(output.isTTY) && environment.TERM !== "dumb";
    this.timer = null;
    this.label = "";
    this.detail = "";
    this.frame = 0;
  }

  stage(label) {
    this.stop();
    this.label = label;
    this.detail = "";
    if (!this.interactive) {
      this.output.write(`coconet: ${label}\n`);
      return;
    }
    this.render();
    this.timer = setInterval(() => this.render(), 100);
    this.timer.unref();
  }

  download(bytes, total) {
    const received = `${(bytes / 1024 / 1024).toFixed(1)} MB`;
    this.detail = Number.isSafeInteger(total) && total > 0 && bytes <= total
      ? `${received} / ${(total / 1024 / 1024).toFixed(1)} MB, ${Math.floor(bytes / total * 100)}%`
      : `${received} downloaded`;
  }

  render() {
    const frame = ["|", "/", "-", "\\"][this.frame++ % 4];
    const line = `${frame} ${this.label}${this.detail ? ` (${this.detail})` : ""}`;
    // Leave the terminal's final column unused to avoid wrapping the spinner.
    const width = Math.max(1, (this.output.columns || 80) - 1);
    this.output.write(`\r\x1b[2K${line.slice(0, width)}`);
  }

  stop() {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
      this.output.write("\r\x1b[2K");
    }
  }
}

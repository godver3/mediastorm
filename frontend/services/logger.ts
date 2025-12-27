/**
 * Logger service that intercepts console.log/warn/error and stores them
 * in a circular buffer for later submission to the backend.
 */

type LogLevel = 'log' | 'warn' | 'error' | 'debug' | 'info';

interface LogEntry {
  timestamp: string;
  level: LogLevel;
  message: string;
}

const MAX_LOG_ENTRIES = 5000;

class Logger {
  private logs: LogEntry[] = [];
  private initialized = false;
  private originalConsole: {
    log: typeof console.log;
    warn: typeof console.warn;
    error: typeof console.error;
    debug: typeof console.debug;
    info: typeof console.info;
  } | null = null;

  /**
   * Initialize the logger by intercepting console methods.
   * Should be called once at app startup.
   */
  init(): void {
    if (this.initialized) {
      return;
    }

    // Store original console methods
    this.originalConsole = {
      log: console.log.bind(console),
      warn: console.warn.bind(console),
      error: console.error.bind(console),
      debug: console.debug.bind(console),
      info: console.info.bind(console),
    };

    // Intercept each console method
    console.log = (...args: unknown[]) => {
      this.capture('log', args);
      this.originalConsole?.log(...args);
    };

    console.warn = (...args: unknown[]) => {
      this.capture('warn', args);
      this.originalConsole?.warn(...args);
    };

    console.error = (...args: unknown[]) => {
      this.capture('error', args);
      this.originalConsole?.error(...args);
    };

    console.debug = (...args: unknown[]) => {
      this.capture('debug', args);
      this.originalConsole?.debug(...args);
    };

    console.info = (...args: unknown[]) => {
      this.capture('info', args);
      this.originalConsole?.info(...args);
    };

    this.initialized = true;
    this.capture('info', ['[Logger] Initialized - capturing console output']);
  }

  /**
   * Capture a log entry into the circular buffer.
   */
  private capture(level: LogLevel, args: unknown[]): void {
    const timestamp = new Date().toISOString();
    const message = args
      .map((arg) => {
        if (arg === null) return 'null';
        if (arg === undefined) return 'undefined';
        if (typeof arg === 'string') return arg;
        if (arg instanceof Error) {
          return `${arg.name}: ${arg.message}${arg.stack ? '\n' + arg.stack : ''}`;
        }
        try {
          return JSON.stringify(arg, null, 2);
        } catch {
          return String(arg);
        }
      })
      .join(' ');

    this.logs.push({ timestamp, level, message });

    // Maintain circular buffer - remove oldest entries if over limit
    if (this.logs.length > MAX_LOG_ENTRIES) {
      this.logs.shift();
    }
  }

  /**
   * Get all captured logs as a formatted string.
   */
  getLogsAsString(): string {
    if (this.logs.length === 0) {
      return '[No logs captured]';
    }

    return this.logs
      .map((entry) => {
        const levelTag = entry.level.toUpperCase().padEnd(5);
        return `[${entry.timestamp}] [${levelTag}] ${entry.message}`;
      })
      .join('\n');
  }

  /**
   * Get the number of captured log entries.
   */
  getLogCount(): number {
    return this.logs.length;
  }

  /**
   * Clear all captured logs.
   */
  clear(): void {
    this.logs = [];
  }
}

// Export singleton instance
export const logger = new Logger();

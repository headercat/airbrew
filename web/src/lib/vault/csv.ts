// CSV parser and format-specific importers for migrating vaults from other
// password managers into Airbrew.
//
// The parser is a minimal RFC 4180 reader (quoted fields, embedded commas,
// newlines and "" escapes) with no third-party dependency, which keeps the
// sensitive password data inside the page and out of a vendored bundle.
// Format detection picks among the common browser/manager export shapes; the
// raw rows are then normalised into a shared CSVParsedItem the store can turn
// into encrypted DraftItems via the existing create path.

export type CSVFormat =
  | "auto"
  | "bitwarden"
  | "chrome"
  | "firefox"
  | "1password";

export type CSVParsedItem = {
  name: string;
  username?: string;
  password?: string;
  url?: string;
  notes?: string;
  totp?: string;
};

// parseCSV reads RFC 4180 CSV text into a row x cell matrix. It accepts CRLF
// and LF line endings and treats a quoted field that spans newlines as part of
// the same record.
export function parseCSV(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let inQuotes = false;
  // Normalise CRLF to LF so we only handle one newline style below.
  const input = text.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
  for (let i = 0; i < input.length; i++) {
    const ch = input[i];
    if (inQuotes) {
      if (ch === '"') {
        if (input[i + 1] === '"') {
          field += '"';
          i++;
        } else {
          inQuotes = false;
        }
      } else {
        field += ch;
      }
    } else {
      if (ch === '"') {
        inQuotes = true;
      } else if (ch === ",") {
        row.push(field);
        field = "";
      } else if (ch === "\n") {
        row.push(field);
        rows.push(row);
        row = [];
        field = "";
      } else {
        field += ch;
      }
    }
  }
  // Flush the trailing field/row when the file does not end in a newline.
  if (field !== "" || row.length > 0) {
    row.push(field);
    rows.push(row);
  }
  return rows.filter((r) => r.length > 1 || (r.length === 1 && r[0] !== ""));
}

// detectFormat inspects the header row to pick the best-matching exporter.
export function detectFormat(headers: string[]): CSVFormat {
  const lower = headers.map((h) => h.toLowerCase().trim());
  const has = (...names: string[]) =>
    names.every((n) => lower.includes(n));
  // 1Password exports use Title (capitalised) and a "Website" column.
  if (lower.includes("title") && lower.includes("website")) return "1password";
  // Firefox includes httpRealm / formActionOrigin.
  if (lower.includes("httprealm") || lower.includes("formactionorigin")) {
    return "firefox";
  }
  // Bitwarden uses folder + login_uri (long export) or name + uri (simple).
  if (
    lower.includes("folder") ||
    lower.includes("login_uri") ||
    lower.includes("login_username")
  ) {
    return "bitwarden";
  }
  // Chrome / Edge / Safari: name,url,username,password[,note].
  if (has("url", "username", "password")) return "chrome";
  return "bitwarden";
}

// readSheet splits the parsed matrix into a header row and body rows, dropping
// empty rows. The format is resolved (auto-detected when needed) and returned
// alongside the body so callers can show what was detected.
export function readSheet(
  rows: string[][],
  format: CSVFormat,
): { headers: string[]; body: string[][]; format: CSVFormat } {
  if (rows.length === 0) return { headers: [], body: [], format: "bitwarden" };
  const headers = rows[0].map((h) => h.trim());
  const resolved = format === "auto" ? detectFormat(headers) : format;
  const body = rows.slice(1).filter((r) => r.some((c) => c.trim() !== ""));
  return { headers, body, format: resolved };
}

// convertRows normalises body rows into CSVParsedItem[] for the chosen format.
export function convertRows(
  headers: string[],
  body: string[][],
  format: CSVFormat,
): CSVParsedItem[] {
  switch (format) {
    case "bitwarden":
      return body.map((r) => mapByHeaders(headers, r, bitwardenMap));
    case "chrome":
      return body.map((r) => mapByHeaders(headers, r, chromeMap));
    case "firefox":
      return body.map((r) => mapByHeaders(headers, r, firefoxMap));
    case "1password":
      return body.map((r) => mapByHeaders(headers, r, onepasswordMap));
    default:
      return body.map((r) => mapByHeaders(headers, r, bitwardenMap));
  }
}

type FieldMap = Record<
  keyof CSVParsedItem,
  (headers: string[], row: string[]) => string | undefined
>;

const bitwardenMap: FieldMap = {
  name: (h, r) => cell(h, r, "name"),
  username: (h, r) => cell(h, r, "login_username", "username"),
  password: (h, r) => cell(h, r, "login_password", "password"),
  url: (h, r) => cell(h, r, "login_uri", "uri", "url"),
  notes: (h, r) => cell(h, r, "notes", "note"),
  totp: (h, r) => cell(h, r, "login_totp", "totp"),
};

const chromeMap: FieldMap = {
  name: (h, r) => cell(h, r, "name", "title"),
  username: (h, r) => cell(h, r, "username"),
  password: (h, r) => cell(h, r, "password"),
  url: (h, r) => cell(h, r, "url", "website"),
  notes: (h, r) => cell(h, r, "note", "notes"),
  totp: () => undefined,
};

const firefoxMap: FieldMap = {
  name: (h, r) => {
    const url = cell(h, r, "url") ?? "";
    try {
      return new URL(url).hostname || url || "Firefox item";
    } catch {
      return url || "Firefox item";
    }
  },
  username: (h, r) => cell(h, r, "username"),
  password: (h, r) => cell(h, r, "password"),
  url: (h, r) => cell(h, r, "url"),
  notes: (h, r) => cell(h, r, "httprealm"),
  totp: () => undefined,
};

const onepasswordMap: FieldMap = {
  name: (h, r) => cell(h, r, "title", "name"),
  username: (h, r) => cell(h, r, "username"),
  password: (h, r) => cell(h, r, "password"),
  url: (h, r) => cell(h, r, "website", "url", "urls"),
  notes: (h, r) => cell(h, r, "notes", "note"),
  totp: (h, r) => cell(h, r, "totp"),
};

function cell(
  headers: string[],
  row: string[],
  ...names: string[]
): string | undefined {
  for (const n of names) {
    const idx = headers.findIndex((h) => h.toLowerCase() === n);
    if (idx >= 0 && row[idx] !== undefined && row[idx] !== "") return row[idx];
  }
  return undefined;
}

function mapByHeaders(
  headers: string[],
  row: string[],
  map: FieldMap,
): CSVParsedItem {
  const name = map.name(headers, row)?.trim() || "Untitled";
  return {
    name,
    username: map.username(headers, row) || undefined,
    password: map.password(headers, row) || undefined,
    url: map.url(headers, row) || undefined,
    notes: map.notes(headers, row) || undefined,
    totp: map.totp(headers, row) || undefined,
  };
}

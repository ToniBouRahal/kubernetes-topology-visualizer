/**
 * Node ids are opaque — `contracts/ids.md` §2, ADR-003 and ADR-006 invariant checklists.
 *
 * An id looks parseable (`k8s:<cluster>:<namespace>:<kind>:<name>`), which is exactly the trap.
 * A consumer that splits one works perfectly until a cluster id contains a colon, a segment is
 * added, or the external form is met — and then it renders a confidently wrong namespace or kind
 * rather than failing.
 *
 * This is enforced against the source rather than through behaviour because the failure is
 * invisible in tests using well-formed ids: `CompareCanvas` parsed ids for an entire phase, with a
 * comment explaining why it was fine, and every test still passed.
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { extname, join, relative, resolve } from "node:path";
import { describe, expect, it } from "vitest";

const SRC = resolve(__dirname, "../src");

/** Generated clients are not hand-written and are checked separately. */
const EXCLUDED = ["api/generated"];

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      out.push(...sourceFiles(full));
    } else if ([".ts", ".tsx"].includes(extname(entry))) {
      out.push(full);
    }
  }
  return out;
}

/**
 * Splitting on ":" or "/" applied to something id-shaped.
 *
 * Deliberately narrow: `"a,b".split(",")` and `text.split(" ")` are ordinary string handling and
 * must not fail this test.
 */
const ID_PARSING = [
  /\.split\(\s*["':]\s*:\s*["']\s*\)/,
  /\.split\(\s*["']:["']\s*\)/,
  /\.split\(\s*["']\/["']\s*\)/,
  /\.split\(\s*\/[:/]\/\s*\)/,
];

describe("node ids are treated as opaque", () => {
  const files = sourceFiles(SRC).filter(
    (f) => !EXCLUDED.some((ex) => relative(SRC, f).startsWith(ex)),
  );

  it("finds source files to check", () => {
    expect(files.length).toBeGreaterThan(5);
  });

  it("no component splits an id on ':' or '/'", () => {
    const offenders: string[] = [];
    for (const file of files) {
      readFileSync(file, "utf8")
        .split("\n")
        .forEach((line, i) => {
          // A comment describing the rule is not a violation of it.
          const code = line.replace(/\/\/.*$/, "").replace(/\/\*.*?\*\//g, "");
          if (ID_PARSING.some((re) => re.test(code))) {
            offenders.push(`${relative(SRC, file)}:${i + 1}  ${line.trim()}`);
          }
        });
    }
    expect(offenders, `ids must stay opaque:\n${offenders.join("\n")}`).toEqual([]);
  });

  it("nothing under api/generated/ is hand-edited", () => {
    // The generated client carries a banner; losing it means someone rewrote the file by hand.
    const generated = sourceFiles(join(SRC, "api/generated"));
    expect(generated.length).toBeGreaterThan(0);
    for (const file of generated) {
      const head = readFileSync(file, "utf8").slice(0, 400);
      expect(head, `${relative(SRC, file)} has lost its generated-file banner`).toMatch(
        /auto[- ]?generated|do not (edit|modify)|openapi-typescript/i,
      );
    }
  });
});

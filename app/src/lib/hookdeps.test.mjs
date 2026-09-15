import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

/**
 * A hook's dependency array must not name a `const` declared further down the
 * same component.
 *
 * WHY THIS EXISTS. A dependency array is evaluated DURING RENDER, at the point
 * the `useEffect(...)` call appears — not when the effect body runs. So a
 * `const` declared later in the component body is in its temporal dead zone at
 * that moment, and the whole page throws before anything reaches the screen:
 *
 *     ReferenceError: Cannot access 'isTaskActive' before initialization
 *
 * That shipped to production in the Stripe Phase 2b web changes. The polling
 * effect was written up beside the other `useEffect`s, ~440 lines above the
 * `const isTaskActive` it depended on, and it took TaskDetail down for every
 * viewer — requester and supporter, every task status. The build was clean
 * (this is legal JavaScript), the typecheck was clean (there is none for web),
 * and the minified name in the production stack trace was `$i`, which said
 * nothing about where to look.
 *
 * It is NOT a bundler artifact and NOT an import cycle: `madge --circular`
 * reports none, and dev mode reproduces it identically. It is plain language
 * semantics, and the only reason it is easy to write is that the effect BODY
 * would have been fine — a callback closes over the binding and runs later.
 *
 * Like pricing.test.mjs, this is a static check rather than a unit test,
 * because the failure mode it guards is "this code exists at all", not "this
 * code computes the wrong thing".
 */

const pagesDir = fileURLToPath(new URL('../pages/', import.meta.url))
const componentsDir = fileURLToPath(new URL('../components/', import.meta.url))

function sourceFiles() {
  const files = []
  for (const dir of [pagesDir, componentsDir]) {
    for (const name of readdirSync(dir)) {
      if (name.endsWith('.jsx')) files.push({ path: dir + name, label: name })
    }
  }
  return files
}

/**
 * Splits a file into top-level function blocks, so a `const` inside one
 * component is never compared against a hook call inside another. Anything
 * before the first top-level function (imports, module constants) is ignored —
 * those are hoisted to module scope and initialized before any render.
 */
function topLevelFunctions(lines) {
  const starts = []
  lines.forEach((line, i) => {
    if (/^(export default |export )?function [A-Za-z_$][\w$]*\s*\(/.test(line)) starts.push(i)
  })
  return starts.map((start, n) => ({
    start,
    end: n + 1 < starts.length ? starts[n + 1] : lines.length,
  }))
}

// `  const foo = …` / `  const { a, b } = …` at the component body's own
// indentation. Deliberately not matching deeper indentation: a const inside a
// nested block cannot be in a dependency array's scope anyway.
function constDeclarations(lines, from, to) {
  const declared = new Map()
  for (let i = from; i < to; i++) {
    const single = /^ {2}const\s+([A-Za-z_$][\w$]*)/.exec(lines[i])
    if (single && !declared.has(single[1])) declared.set(single[1], i)
    const destructured = /^ {2}const\s+\{([^}]*)\}/.exec(lines[i])
    if (destructured) {
      for (const part of destructured[1].split(',')) {
        const name = part.split(':').pop().trim().replace(/\s*=.*$/, '')
        if (/^[A-Za-z_$][\w$]*$/.test(name) && !declared.has(name)) declared.set(name, i)
      }
    }
  }
  return declared
}

// The `}, [a, b.c, d])` tail of a hook call, which is how every dependency
// array in this codebase is written.
const DEPS_ARRAY = /\}\s*,\s*\[([^\]]*)\]\s*\)/

test('no hook dependency array reads a const declared later in the same component', () => {
  for (const file of sourceFiles()) {
    const lines = readFileSync(file.path, 'utf8').split('\n')

    for (const fn of topLevelFunctions(lines)) {
      const declared = constDeclarations(lines, fn.start, fn.end)

      for (let i = fn.start; i < fn.end; i++) {
        const match = DEPS_ARRAY.exec(lines[i])
        if (!match) continue

        for (const raw of match[1].split(',')) {
          // Only the root identifier matters: `task?.status` is safe as long
          // as `task` itself is initialized.
          const root = /^[A-Za-z_$][\w$]*/.exec(raw.trim())?.[0]
          if (!root) continue

          const declaredAt = declared.get(root)
          if (declaredAt !== undefined && declaredAt > i) {
            assert.fail(
              `${file.label}:${i + 1} — dependency array names "${root}", which is a const ` +
                `declared at line ${declaredAt + 1}. Dependency arrays are evaluated during ` +
                `render, so this throws "Cannot access '${root}' before initialization" and ` +
                `takes the whole page down. Move the hook below the declaration.`
            )
          }
        }
      }
    }
  }
})

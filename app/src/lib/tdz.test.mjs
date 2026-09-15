import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { parse } from '@babel/parser'
import _traverse from '@babel/traverse'

const traverse = _traverse.default ?? _traverse

/**
 * Nothing evaluated DURING RENDER may read a `const` declared further down the
 * same function.
 *
 * This is the temporal-dead-zone rule, and it has now taken the web app down
 * in production twice:
 *
 *   isTaskActive     a hook's DEPENDENCY ARRAY, ~440 lines above the const
 *   scheduledAtISO   a hook's ARGUMENT OBJECT, 9 lines above the const
 *
 * Both are legal JavaScript, both build clean, and both throw
 * "Cannot access 'X' before initialization" before a single pixel renders —
 * with a minified name in the stack trace that points nowhere.
 *
 * THE FIRST VERSION OF THIS GUARD CAUGHT ONLY THE FIRST ONE. It scanned for
 * the literal `}, [deps])` tail of a hook call, which is the shape the first
 * bug happened to have. It encoded an instance instead of the rule, so the
 * second bug — same cause, same failure, same file type, one argument to the
 * left — walked straight past it.
 *
 * So this version states the rule and lets a parser find every shape of it:
 *
 *   for every binding in a function's own scope,
 *     for every reference to it that appears before its declaration,
 *       if that reference is NOT inside a nested function, it is a crash.
 *
 * The "nested function" exemption is what makes this precise rather than
 * merely strict. A callback closes over the binding and runs later, so an
 * effect BODY reading a const declared below it is completely fine — that is
 * exactly why the first bug was so easy to write, and a guard that flagged it
 * would be noise nobody would keep.
 */

const DIRS = ['../pages/', '../components/', '../auth/', '../hooks/', '../providers/']

function sourceFiles() {
  const files = []
  for (const dir of DIRS) {
    const base = fileURLToPath(new URL(dir, import.meta.url))
    for (const name of readdirSync(base)) {
      if (/\.(jsx|js)$/.test(name) && !name.endsWith('.test.mjs')) {
        files.push({ path: base + name, label: name })
      }
    }
  }
  return files
}

/** The outermost statement a binding is declared by — stepping up through the
 *  VariableDeclaration and any `export` wrapping it. */
function statementOf(bindingPath) {
  let node = bindingPath
  while (node && !node.isStatement()) node = node.parentPath
  while (node?.parentPath?.isExportNamedDeclaration?.()) node = node.parentPath
  return node?.node
}

function lineOf(code, index) {
  return code.slice(0, index).split('\n').length
}

/** Every temporal-dead-zone read in one file, as human-readable findings. */
export function findTDZReads(code, label = 'source') {
  const ast = parse(code, {
    sourceType: 'module',
    plugins: ['jsx'],
    errorRecovery: true,
  })

  const findings = []

  function check(scopePath) {
    for (const [name, binding] of Object.entries(scopePath.scope.bindings)) {
      // `var` is hoisted and `function` declarations are fully hoisted, so
      // neither can be in a dead zone. Only let/const can.
      if (binding.kind !== 'const' && binding.kind !== 'let') continue

      // The whole STATEMENT, not just the declarator: for
      // `export const useLoader = () => …` Babel counts the exported name as a
      // reference, and it sits before the declarator's own start. Comparing
      // against the statement — and ignoring anything inside it — keeps that
      // out without loosening the rule anywhere that matters.
      const stmt = statementOf(binding.path)
      const declStart = stmt?.start
      const declEnd = stmt?.end
      if (declStart == null) continue

      for (const ref of binding.referencePaths) {
        const refStart = ref.node.start
        if (refStart == null || refStart >= declStart) continue
        if (declEnd != null && refStart >= declStart && refStart < declEnd) continue

        // Inside a nested function? Then it runs later, and reading a const
        // declared below is correct and common.
        const owner = ref.getFunctionParent()
        if (owner && owner.node !== scopePath.node) continue

        findings.push({
          file: label,
          name,
          usedAtLine: lineOf(code, refStart),
          declaredAtLine: lineOf(code, declStart),
        })
      }
    }
  }

  traverse(ast, {
    Program(path) {
      check(path)
    },
    Function(path) {
      check(path)
    },
  })

  return findings
}

test('nothing read during render is declared later in the same function', () => {
  const problems = []
  for (const file of sourceFiles()) {
    const code = readFileSync(file.path, 'utf8')
    problems.push(...findTDZReads(code, file.label))
  }

  if (problems.length > 0) {
    const detail = problems
      .map(
        (p) =>
          `  ${p.file}:${p.usedAtLine} reads "${p.name}", declared at line ${p.declaredAtLine}`
      )
      .join('\n')
    assert.fail(
      `Temporal dead zone — these throw "Cannot access 'X' before initialization" ` +
        `during render and take the whole page down:\n${detail}\n\n` +
        `Move the declaration above its first render-time use. (A reference inside a ` +
        `callback would be fine; these are not.)`
    )
  }
})

// The guard's own regression test. Both shapes below shipped to production;
// if a future rewrite of the analysis stops catching either, it has quietly
// become decorative again.
test('the guard catches both shapes that actually reached production', () => {
  const depsArray = `
    export default function Screen() {
      useEffect(() => {
        if (!isTaskActive) return
      }, [isTaskActive, id])
      const isTaskActive = task?.status === 'open'
      return null
    }
  `
  const hookArgument = `
    export default function Screen() {
      const estimate = useTaskEstimate({
        category: 'delivery',
        scheduledAt: scheduledAtISO,
      })
      const scheduledAtISO = useMemo(() => '', [])
      return estimate
    }
  `
  for (const [shape, code] of [['dependency array', depsArray], ['hook argument', hookArgument]]) {
    const found = findTDZReads(code, shape)
    assert.equal(found.length, 1, `${shape}: expected exactly one finding, got ${found.length}`)
  }
})

// And the exemption, which is what keeps it from crying wolf.
test('a reference inside a callback is not flagged', () => {
  const deferred = `
    export default function Screen() {
      useEffect(() => {
        console.log(later)
      }, [])
      function onSubmit() {
        return later
      }
      const handler = () => later
      const later = 1
      return later
    }
  `
  assert.deepEqual(findTDZReads(deferred, 'deferred'), [])
})

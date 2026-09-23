// Entry for `node --import ./test/register.mjs --test`. Installs the resolve
// hook that swaps the Expo native modules for the in-memory fakes in
// ./fakes, and the one global React Native defines that the modules under
// test read.
import { register } from "node:module";

globalThis.__DEV__ = false;
register("./hooks.mjs", import.meta.url);

export const __tasks = new Map();
export function defineTask(name, executor) {
  __tasks.set(name, executor);
}

export const __store = new Map();
export function __reset() {
  __store.clear();
}
export async function getItemAsync(key) {
  return __store.has(key) ? __store.get(key) : null;
}
export async function setItemAsync(key, value) {
  __store.set(key, value);
}
export async function deleteItemAsync(key) {
  __store.delete(key);
}

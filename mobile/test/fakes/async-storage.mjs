export const __store = new Map();
export function __reset() {
  __store.clear();
}
export default {
  async getItem(key) {
    return __store.has(key) ? __store.get(key) : null;
  },
  async setItem(key, value) {
    __store.set(key, value);
  },
  async removeItem(key) {
    __store.delete(key);
  },
};

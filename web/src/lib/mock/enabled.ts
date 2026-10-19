// Mock 開關。構建期注入的公開變數（NEXT_PUBLIC_ 字首才在瀏覽器可讀）。
// Vercel 上設 NEXT_PUBLIC_MOCK=1 即整站走 mock、無需後端。
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";

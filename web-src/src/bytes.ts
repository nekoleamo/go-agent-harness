// 字节口径:面板上一切标着「字节」的数(计数与上限判断)都必须与 Go 服务端同一把尺子。
//
// 为什么不能直接用 String.length:JS 数的 length 是 **UTF-16 码元**,Go 的 len(string) 数的是
// **UTF-8 字节** —— 同一个汉字 JS 算 1、Go 算 3(emoji 这类非 BMP 字符两边都不对:JS 2、UTF-8 4)。
// 指令文本大多是中文,这个差就是 3 倍:面板写着「12000 / 32768 字节」,服务端按 36000 字节**拒掉**,
// 用户看到的数字与"为什么存不进去"完全对不上;反过来客户端的前置校验也形同虚设(该拦的没拦,
// 只能靠服务端 400 兜底,把一句人话变成一条原始报错)。
export function byteLength(s: string): number {
  return new TextEncoder().encode(s).length
}

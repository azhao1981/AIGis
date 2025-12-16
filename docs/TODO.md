
问题3：3 转换引擎：我们要设计一个转换引擎，替换开发低效的 openai.go 硬编码替换，他的功能应该包括 
1) 把同一个provider的消息做PII替换或是key转换，加header 等等
2) 不同的 provider 转换，和PII替换，比如openai 转到 dify 
3) 引擎配置可以放在数据库或是直接调用

引擎配置大约如下：
  request
    from Setting: ak 
    to setting: ak base_url 
    rules: [替换手机号 替换邮箱]
    path openai dify cluade 随机 之类
  reresponse
    path /openai /dify /cluade

  
问题4：4 替换器优化，增加 方法规则 到这里MVP 完成
问题5：provider 添加 openai dify cluade

NEXT：
1. 记录 敏感信息
2. 并发监控
3. 消息回溯（往回替换）

## rule: overwrite 补充

* 补充 (If missing, add):
       * `default` (Most common for "use this if nothing is there")
       * `fill` or fill_missing
       * `ensure` (Ensure a value exists)
       * `supplement` (Less common in code variables, more in docs)

* 替换 (If exists, replace / Force update):
       * `overwrite` (Standard for "write over regardless")
       * `override` (Common in inheritance or config layering)
       * `replace`

  Summary of logic:

   1. Overwrite / Replace: "Whether it exists or not, write the new value." (Ignoring the old value).
   2. Default / Fill: "Only write the value if the current one is empty/null."

那么一个简单的openai基本的规则如下：
overwrite api_key "Bearer "+p.apiKey
overwrite base_url 
replace message PII:phone
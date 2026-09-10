// 有旗子素材的地区代码。旗子本身一面一个文件在 web/dist/flags/,用到哪面取哪面。
// 素材来自 flag-icons(MIT,Panayiotis Lipiridis)的 flags/1x1 方形版,做了无损精简;许可与出处见仓库根的 NOTICE。
// 不用旗帜 emoji:Windows 至今没有旗帜字体,🇭🇰 会被画成两个字母 HK,四端里最主要的那端反而最难看。
const FLAG_SET = new Set(["HK","MO","TW","CN","JP","KR","SG","TH","VN","MY","ID","PH","IN","KZ","AE","SA","IL","TR","PK","BD","NP","LK","MM","KH","LA","MN","UZ","GE","AM","AZ","QA","KW","BH","OM","JO","LB","IQ","IR","GB","IE","FR","DE","NL","BE","LU","ES","PT","IT","CH","AT","CZ","SK","PL","HU","RO","BG","GR","RS","HR","SI","BA","MK","AL","ME","UA","RU","BY","MD","SE","NO","DK","FI","IS","EE","LV","LT","MT","CY","US","CA","MX","BR","AR","CL","CO","PE","UY","PY","BO","EC","VE","CR","PA","GT","DO","CU","AU","NZ","ZA","EG","NG","KE","MA","DZ","TN","GH","ET","TZ","UG","AO","MZ","SN","CI","CM","ZW"]);
const flagURL = code => FLAG_SET.has(code) ? 'flags/' + code.toLowerCase() + '.svg' : '';

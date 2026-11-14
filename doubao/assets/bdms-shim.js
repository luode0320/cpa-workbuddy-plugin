var __store = {};
function MK(){ return { getItem:function(k){return Object.prototype.hasOwnProperty.call(__store,k)?__store[k]:null;}, setItem:function(k,v){__store[k]=String(v);}, removeItem:function(k){delete __store[k];}, clear:function(){__store={};}, key:function(i){return Object.keys(__store)[i];}, get length(){return Object.keys(__store).length;} }; }
var __lsReads = [];
var localStorage = (function(){ var o = MK(); var g = o.getItem; o.getItem = function(k){ var v = g.call(o, k); __lsReads.push(String(k) + '=' + (v===null?'null':String(v).slice(0,12))); return v; }; return o; })();
var sessionStorage = MK();
var location = { href:'https://www.doubao.com/chat/', origin:'https://www.doubao.com', protocol:'https:', host:'www.doubao.com', hostname:'www.doubao.com', pathname:'/chat/', search:'', hash:'', port:'', ancestorOrigins:[], toString:function(){return 'https://www.doubao.com/chat/';} };
var navigator = { userAgent:'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.7727.149 Safari/537.36', appVersion:'5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.7727.149 Safari/537.36', language:'zh-CN', languages:['zh-CN','zh'], platform:'Win32', vendor:'Google Inc.', webdriver:false, cookieEnabled:true, plugins:[], mimeTypes:[], hardwareConcurrency:8, deviceMemory:8, maxTouchPoints:0, onLine:true, javaEnabled:function(){return false;}, sendBeacon:function(){return true;}, doNotTrack:null };
function ELE(t){ return { tagName:t, style:{}, attributes:{}, childNodes:[], setAttribute:function(k,v){this.attributes[k]=v;}, getAttribute:function(k){return this.attributes[k]||null;}, removeAttribute:function(){}, appendChild:function(c){this.childNodes.push(c);return c;}, removeChild:function(){}, addEventListener:function(){}, removeEventListener:function(){}, getContext:function(){ return null; }, getBoundingClientRect:function(){ return {top:0,left:0,right:0,bottom:0,width:0,height:0,x:0,y:0}; }, remove:function(){}, cloneNode:function(){return ELE(t);}, innerHTML:'', outerHTML:'', src:'', href:'', width:0, height:0, clientWidth:0, clientHeight:0, offsetWidth:0, offsetHeight:0, children:[], parentNode:null, insertBefore:function(){}, querySelector:function(){return null;}, querySelectorAll:function(){return [];} }; }
var document = { cookie:'', referrer:'', title:'doubao', readyState:'complete', visibilityState:'visible', hidden:false, characterSet:'UTF-8', domain:'www.doubao.com', URL:'https://www.doubao.com/chat/',
  createElement:function(t){return ELE(t);}, createElementNS:function(ns,t){return ELE(t);}, createTextNode:function(){return ELE('#text');}, createDocumentFragment:function(){return ELE('#fragment');},
  documentElement:ELE('html'), body:ELE('body'), head:ELE('head'),
  addEventListener:function(){}, removeEventListener:function(){}, hasFocus:function(){return true;},
  getElementsByTagName:function(){return [];}, getElementsByClassName:function(){return [];}, getElementById:function(){return null;},
  querySelector:function(){return null;}, querySelectorAll:function(){return [];},
  write:function(){}, open:function(){}, close:function(){}, evaluate:function(){return null;} };
var chrome = { runtime:{ id:undefined } };
var performance = { now:function(){return Date.now();}, timeOrigin:Date.now(), timing:{navigationStart:Date.now()}, getEntriesByType:function(){return [];}, getEntriesByName:function(){return [];}, mark:function(){}, measure:function(){} };
var screen = { width:1920, height:1080, availWidth:1920, availHeight:1040, colorDepth:24, pixelDepth:24, orientation:{type:'landscape-primary',angle:0} };
var history = { length:2, state:null, pushState:function(){}, replaceState:function(){}, back:function(){}, forward:function(){}, go:function(){} };
var __Q = [];
function setTimeout(fn,ms){ __Q.push({fn:fn,ms:ms||0}); return __Q.length; }
function clearTimeout(){}
function setInterval(){return 0;}
function clearInterval(){}
function requestAnimationFrame(fn){ return setTimeout(fn,16); }
function cancelAnimationFrame(){}
function addEventListener(){}
function removeEventListener(){}
function dispatchEvent(){return true;}
function getComputedStyle(){ return { getPropertyValue:function(){return '';}, length:0 }; }
var XMLHttpRequest = __QXHR;
var fetch = __QF;
var MutationObserver = function(){ this.observe=function(){}; this.disconnect=function(){}; this.takeRecords=function(){return [];}; };
var Image = function(){ this.src=''; this.addEventListener=function(){}; };
var Worker = function(){ this.postMessage=function(){}; this.addEventListener=function(){}; this.terminate=function(){}; };
var WebSocket = function(){ this.send=function(){}; this.close=function(){}; this.addEventListener=function(){}; };
var btoa = function(s){ s = String(s); var c = []; for (var i=0;i<s.length;i++) c.push(s.charCodeAt(i) & 255); return goBtoaCodes(c); };
var __bytesToStr = function(codes){ var out = ''; for (var i=0;i<codes.length;i++) out += String.fromCharCode(codes[i]); return out; };
var atob = function(s){ var c = goAtobCodes(String(s)); if (!c) throw new Error('atob failed'); return __bytesToStr(c); };
var TextEncoder = function(){ this.encoding='utf-8'; this.encode=function(s){ return goUtf8Encode(String(s)); }; };
var TextDecoder = function(){ this.encoding='utf-8'; this.decode=function(b){ return goUtf8Decode(b); }; };
var crypto = { getRandomValues:function(arr){ var r = goRandomBytes(arr.length); for (var i=0;i<arr.length;i++){ arr[i]=r[i]; } return arr; }, randomUUID:function(){ return goRandomUUID(); }, subtle:undefined };
var URL = function(u, base){ this.href = String(u); this.toString=function(){return this.href;}; };
URL.createObjectURL = function(){ return 'blob:mock'; };
URL.revokeObjectURL = function(){};
var URLSearchParams = function(s){ this._s = String(s||''); this.get=function(){return null;}; this.append=function(){}; this.toString=function(){return this._s;}; };
var matchMedia = function(){ return { matches:false, addListener:function(){}, removeListener:function(){}, addEventListener:function(){}, removeEventListener:function(){} }; };
var __chunkModules = {};
var __cache = {};
function __req(id) {
  if (__cache[id]) { return __cache[id].exports; }
  var m = { exports: {} };
  __cache[id] = m;
  var f = __chunkModules[id];
  if (f) { f.call(m.exports, m, m.exports, __req); }
  return m.exports;
}
__chunkModules['FJc'] = function(m, e, r){ e.env = {NODE_ENV:'production'}; e.platform='browser'; e.browser=true; e.nextTick=function(f){f();}; e.cwd=function(){return '/';}; };
var rspackChunkapp_flow_chat = {
  push: function(arg){
    try {
      var ids = arg[0], mods = arg[1];
      for (var k in mods) { __chunkModules[k] = mods[k]; }
      if (__chunkModules['x6h']) { __req('x6h'); }
    } catch(e) { window.__chunkerr = String(e && e.stack || e); }
  }
};
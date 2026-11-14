// 可观测 XHR / fetch 桩：方法挂在 prototype 上，才能被 SDK 的 prototype hook 拦截
function QXHR(){
  this.readyState = 0; this.status = 0; this.response = ''; this.responseText = ''; this.responseURL = '';
  this._url = ''; this._hdrs = {}; this._body = ''; this._m = '';
}
QXHR.UNSENT = 0; QXHR.OPENED = 1; QXHR.HEADERS_RECEIVED = 2; QXHR.LOADING = 3; QXHR.DONE = 4;
QXHR.prototype.open = function(m, u){ this._m = String(m); this._url = String(u); this.readyState = 1; };
QXHR.prototype.setRequestHeader = function(k, v){ this._hdrs[k] = v; };
QXHR.prototype.getAllResponseHeaders = function(){ return ''; };
QXHR.prototype.getResponseHeader = function(){ return null; };
QXHR.prototype.addEventListener = function(){};
QXHR.prototype.removeEventListener = function(){};
QXHR.prototype.abort = function(){};
QXHR.prototype.overrideMimeType = function(){};
QXHR.prototype.send = function(b){
  this._body = String(b || '');
  this.readyState = 2;
  window.__lastXhr = { method: this._m, url: this._url, hdrs: this._hdrs, body: this._body };
};
var __QXHR = QXHR;

function QFetch(input, init){
  var u = (typeof input === 'string') ? input : (input && input.url ? input.url : String(input));
  window.__lastFetch = { url: u };
  return { then:function(){ return this; }, catch:function(){ return this; }, finally:function(){ return this; } };
}
var __QF = QFetch;
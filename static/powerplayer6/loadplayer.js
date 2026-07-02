(function(window, undefined) {
    if (window.power_player) {
        window.power_player.showPlayer();
        return;
    };
    
    var webAddress="";
    var scripts = document.getElementsByTagName("script");
    
    for (var i = 0; i < scripts.length; i = i + 1) {
        var script = scripts[i];
        if (script.src.indexOf("loadplayer.js") == -1) {
            continue;
        }
        var src = script.src;
        //获取参数
        var params = src.split("?")[1].split("&");
        var result = {};
        for (var j = 0; j < params.length; j = j + 1) {
            var key_value = params[j].split("=");
            var key = (key_value[0] + "").replace(/(^\s*)|(\s*$)/g, ""); // trim
            result[key] = key_value[1];
        }
        var params = result;
        webAddress=params.serverIp+":"+params.serverPort;
    }
    
    loadJS("http://"+webAddress+"/powercms/web/player/powerplayer6/powerplayer.min.js",function (){
        loadJS("http://"+webAddress+"/powercms/web/player/powerplayer6/player.js",function (){
                window.power_player.showPlayer();
        });
    });
})(window);

function loadJS(url, success) {
      var domScript = document.createElement('script');
      domScript.src = url;
      success = success || function(){};
      domScript.onload = domScript.onreadystatechange = function() {
        if (!this.readyState || 'loaded' === this.readyState || 'complete' === this.readyState) {
          success();
        }
      }
      document.getElementsByTagName('head')[0].appendChild(domScript);
}

/*
PowerPlayer
v1.0.0
*/
(function(window, undefined) {
    var document = window.document;
    if (window.power_player) {
        window.power_player.showPlayer();
        return;
    }
    
    function NewGuid() {
        var guid = "";
        for (var i = 1; i <= 32; i++){
          var n = Math.floor(Math.random()*16.0).toString(16);
          guid += n;
        }
        return guid;    
    }
    var STREAM_ID = NewGuid();  
    // player
    function Player() {};

    // Use prototype to init function
    Player.prototype = {
        videoInfo: new Array(),
        isIE: function() {
            return navigator.userAgent.match(/MSIE/i) != null;
        },
        isIPad: function() {
            return navigator.userAgent.match(/iPad/i) != null;
        },
        isIPhone: function() {
            return navigator.userAgent.match(/iPhone/i) != null;
        },
        isAndroid: function() {
            return navigator.userAgent.match(/Android/i) != null;
        },
        isAndroid2: function() {
            return navigator.userAgent.match(/Android 2/i) != null;
        },
        isSymbianOS: function() {
            return navigator.userAgent.match(/SymbianOS/i) != null;
        },
        isWindowsPhoneOS: function() {
            return navigator.userAgent.match(/Windows Phone/i) != null;
        },
        showPlayer: function() {
            var scripts = document.getElementsByTagName("script");
            for (var i = 0; i < scripts.length; i = i + 1) {
                var script = scripts[i];
                if (script.src.indexOf("loadplayer.js") == -1) {
                    continue;
                }
                
                var src = script.src;
                script.src = "";
                //获取参数
                var params = this.getParam(src.split("?")[1]);
                //创建一个层
                var video = document.createElement("div");
                var randomid = Math.ceil(Math.random() * 10000000);
                video.id = "video_" + params.vid;// + "_" + randomid;
            
                video.style.width = params.width;
                video.style.height = params.height;
                if (this.isMoble()) {
                    video.style.position = "relative";
                }
                video.innerHTML = "";
                params.divid = video.id;
                // save video params
                this.videoInfo.push(params);
                //替换原有的script标签为video标签
                script.parentNode.replaceChild(video, script);
                //从服务器获取信息
                if (this.isAndroid() && !this.isAndroid2()) {
                    this.jsonp("http://"+params.serverIp+":"+params.serverPort+"/powercms/web/Play-getPlayUrl.action?id=" + params.vid + "&siteid=" + params.siteId + "&divid=" + params.divid + "&progid="+params.progid+"&autostart="+params.autostart, "power_player.showPlayerView");
                } else if (this.isIPhone() || this.isIPad()) {
                     this.jsonp("http://"+params.serverIp+":"+params.serverPort+"/powercms/web/Play-getPlayUrl.action?id=" + params.vid + "&siteid=" + params.siteId + "&divid=" + params.divid + "&progid="+params.progid+"&autostart="+params.autostart, "power_player.showPlayerView");
                } else {
                    this.jsonp("http://"+params.serverIp+":"+params.serverPort+"/powercms/web/Play-getPlayUrl.action?id=" + params.vid + "&siteid=" + params.siteId + "&divid=" + params.divid + "&progid="+params.progid+"&autostart="+params.autostart, "power_player.showPlayerView");
                }
            }
        },
        getParam: function(queryString) {
            var params = queryString.split("&");
            var result = {};
            for (var i = 0; i < params.length; i = i + 1) {
                var key_value = params[i].split("=");
                var key = (key_value[0] + "").replace(/(^\s*)|(\s*$)/g, ""); // trim
                result[key] = key_value[1];
            }
            params.height = params.width / 4 * 3;
            result.userAgent = this.getUserAgent();
            return result;
        },
        getUserAgent: function() {
            var userAgent = navigator.userAgent.match(/MSIE|Firefox|iPad|iPhone|Android|SymbianOS/);
            var winPhoneUA = navigator.userAgent.match(/Windows Phone/);
            if (winPhoneUA == "Windows Phone") {
                userAgent = winPhoneUA;
            }
            if (userAgent) {
                return userAgent;
            } else {
                return "other";
            }
        },
        isMoble: function() {
            var userAgent = navigator.userAgent.match(/iPhone|Android|SymbianOS|Windows Phone/);
            if (userAgent) {
                return true;
            } else {
                return false;
            }
        },
        jsonp: function(url, callback) {
            // use setTimeout to handle multiple requests problem, force them in
            // a queue
            setTimeout(function() {
                var head = document.getElementsByTagName("head")[0] || document.documentElement;
                var script = document.createElement("script");
                // add the param callback to url, and avoid cache
                script.src = url + "&callback=" + callback + "&r=" + Math.random() * 10000000;
                // Use insertBefore instead of appendChild to circumvent an IE6
                // bug.
                // This arises when a base node is used.
                head.insertBefore(script,head.firstChild);

                script.onload = script.onreadystatechange = function() {
                    // use /loaded|complete/.test( script.readyState ) to test
                    // IE6 ready,!this.readyState to test FF
                    if (!this.readyState || /loaded|complete/.test(script.readyState)) {
                        // Handle memory leak in IE
                        script.onload = script.onreadystatechange = null;
                        if (head && script.parentNode) {
                            head.removeChild(script);
                        }
                    }
                };
            },
            0);
        },
        setupFlashPlayer: function(video) {
		
            powerplayer(video.divid).setup({
                modes: [
                {type: "flash", src: video.weburlparam+'/web/player/powerplayer6/powerplayer.swf'},
                {type: "html5"}
                ],
                //flashplayer: video.weburlparam+'/web/player/powerplayer6/powerplayer.swf',
                skin: video.weburlparam+'/web/player/powerplayer6/skin.zip',
                plugins: video.weburlparam+'/web/player/powerplayer6/shortcuts.swf,'+video.weburlparam+'/web/player/powerplayer6/captions.swf',
                fileid:video.fileid,
                contentid:video.contentid,
                siteid:video.siteid,
                file:video.filePath,
                backupservers: video.backupServers,
                streamid: STREAM_ID,
                code: video.code,
                'logo.file': video.logofile,
                'logo.margin': video.logomargin,
                'logo.hide':video.logohide,
                'logo.position': video.logoposition,
                'logo.timeout': video.logotimeout,
                image: video.image,
                username: video.username,
                headtime: video.headtime,
                bottomtime: video.bottomtime,
                starttime: video.starttime,
                endtime: video.endtime,
                title: video.title,
                rid: video.rid,
                duration: video.duration,
                filesize:video.filesize,
                headlength: '1024',
                tracker: video.trackerServer,
                rtmfpserver: video.rtmfpServer,
                statisticsserver: video.statisticsServer,
                weburlparam: video.weburlparam,
                backcolor: '161616',
                showrighttoolbar: true,
                'controlbar.position': 'bottom',
                autostart: video.autostart,
                height: '100%',
                width: '100%',              
                provider:video.provider,
                'http.startparam': 'start',
                useflashsock:true
            });
        },
        setupHTML5Player: function(video) {
            powerplayer(video.divid).setup({
                skin: video.weburlparam+'/web/player/powerplayer6/skin/skin.xml',
                fileid:video.fileid,
                siteid:video.siteid,
                entityid:video.contentid,
                contentid:video.contentid,
                duration:video.duration,
                filesize:video.filesize,
                weburlparam:video.weburlparam,      
                file:video.filePath,    
                title:video.title,              
                backupservers:video.backupServers,
                statisticsserver:video.statisticsServer,
                streamid:STREAM_ID,
                memberName:video.username,
                height: '100%',
                width: '100%',
                autostart:video.autostart,
                provider: 'http',
                'http.startparam': 'start',
                modes: [{ type: "html5" }]
             });    
        },
        setupHTML5PlayerforIPhone: function(video) {
            powerplayer(video.divid).setup({
                fileid:video.fileid,
                siteid:video.siteid,
                entityd:video.contentid,   
                contentid:video.contentid,
                duration:video.duration,
                filesize:video.filesize,
                weburlparam:video.weburlparam,      
                file:video.filePath,    
                title:video.title,              
                backupservers:video.backupServers,
                statisticsserver:video.statisticsServer,
                streamid:STREAM_ID,
                memberName:video.username,
                height: '100%',
                width: '100%',
                autostart:false,
                provider: 'http',
                'http.startparam': 'start',
                modes: [{ type: "html5" }]
             });    
        },
        showPlayerView: function(video) {
            var video_div = document.getElementById(video.divid);
            //alert(video_div.style.width);
            for (var i = 0; i < this.videoInfo.length; i++) {
                var params = this.videoInfo[i];
                if (params.divid == video.divid) {
                    // show video
                    if (this.isIPhone()|| this.isIPad() ) {
                        if(video.format=="hls"){
                            //video.divid.substring(video.divid.indexOf(6)) 原因是video.divid==video_id
                            video.filePath=video.weburlparam+'/web/'+video.divid.substring(6)+'/index.m3u8';
                        }
                        this.setupHTML5PlayerforIPhone(video);
                    } else if (this.isAndroid() || this.isWindowsPhoneOS()) {
                        if(video.format=="hls"){
                            video.filePath=video.weburlparam+'/web/'+video.divid.substring(6)+'/index.m3u8';
                        }
                        this.setupHTML5Player(video);
                    } else {
                        if(video.format=="hls") {
                            video.filePath=video.weburlparam+'/web/'+video.divid.substring(6)+'/index.m3u8';
                        }

                        if(!this.CheckFlash2()) {//判断是否有flash
                           var isIE = (navigator.appVersion.indexOf("MSIE") >= 0);
                            
                            var appVersion=navigator.appVersion;
                            if ((window.applicationCache && !isIE)||appVersion.indexOf("Chrome")>-1) { //如果支持html5,用html5播放//ie浏览器不支持
								
							   this.setupFlashPlayer(video);
                            } else {
                                var noflash="<div style=\"width: 100%;height:100%; margin-bottom:19px; margin-top:-19px; background-color: black;\"><div style=\"width: 100%;height:30%;\"></div><table width=\"100%\" border=\"0\" bgcolor=\"black\">" +
                                        "<tr><td width=\"15%\" rowspan=\"3\" align=\"right\" bgcolor=\"black\"></td>" +
                                        "<td width=\"55%\" align=\"left\" style=\"font-size:12px;color:#99B6D6;line-height:17px;\">您没有安装flash播放器或flash播放器的版本低于10.0.0</td><td width=\"15%\" rowspan=\"3\" align=\"right\" bgcolor=\"black\"></td></tr>" +
                                        "<tr>  <td align=\"left\" style=\"font-size:12px;color:#99B6D6;line-height:17px;\">为了您正常观看视频，请先安装 <a href=\"http://get.adobe.com/cn/flashplayer\" style=\"color:#CBDEF0\" target=\"_blank\">最新版Adobe FLASH播放器</a> </td></tr>" +
                                        "<tr><td align=\"left\" style=\"font-size:12px;color:#99B6D6;line-height:17px;\">安装完成后请刷新页面或按F5</td></tr> </table></div>";
                                    
                                    var v_div=document.getElementById(video.divid);
                                    v_div.innerHTML=noflash;
                            }
                        } else {
                            this.setupFlashPlayer(video);
                        }
                    }
                    return;
                }
            }
        },
        flashChecker:function() {
            var hasFlash = 0;         //是否安装了flash
            var flashVersion = 0;     //flash版本
            var v2=0; 
            var isIE = (navigator.appVersion.indexOf("MSIE") >= 0);;      //是否IE浏览器
            if(isIE) {
                var swf = new ActiveXObject('ShockwaveFlash.ShockwaveFlash'); 
                if(swf) {
                    hasFlash=1;
                    VSwf=swf.GetVariable("$version");       
                    flashVersion=parseInt(VSwf.split(" ")[1].split(",")[0]);
                    v2=parseInt(VSwf.split(" ")[1].split(",")[1]);
                }
            } else {
                if (navigator.plugins && navigator.plugins.length > 0) {
                    var swf=navigator.plugins["Shockwave Flash"];
                    if (swf) {
                        hasFlash=1;
                        var words = swf.description.split(" ")[2];
                    
                        flashVersion=words.split(".")[0];
                        v2=words.split(".")[1];
                    }
                }
            }
            return {f:hasFlash,v:flashVersion,v2:v2};
        },
        CheckFlash2:function() {
            try {
                var fls = this.flashChecker();
                
                if(fls.f == 0) {
                    return false;
                }

                if(fls.v < 10) {
                    return false;
                }else if(fls.v==10&&fls.v2<1){
                    return false;
                }
                return true;
            }catch(e) {
                return false;
            }
        }
    };
   
    // Expose to global
    window.power_player = new Player();
    // End self-executing function
})(window);